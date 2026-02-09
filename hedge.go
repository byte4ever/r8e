package r8e

import (
	"context"
	"time"
)

// Pattern: Hedged Request — after a delay, fire a second concurrent attempt.
// The first response wins; the other is cancelled. This reduces tail latency
// by racing redundant requests.

type (
	// hedgeResult holds the outcome of a hedged call attempt.
	hedgeResult[T any] struct {
		val       T
		err       error
		isPrimary bool
	}

	// HedgeParams holds the configuration for a hedged request.
	HedgeParams struct {
		Clock Clock
		Hooks *Hooks
		Delay time.Duration
	}
)

// DoHedge executes fn and, if it hasn't completed after delay, fires a second
// concurrent attempt. The first response wins; the other is cancelled.
//
//nolint:ireturn // generic type parameter T, not an interface
func DoHedge[T any](
	ctx context.Context,
	fn func(context.Context) (T, error),
	params HedgeParams,
) (T, error) {
	var zero T

	// If the parent context is already done, return its error immediately.
	if ctx.Err() != nil {
		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}

	// Buffered channel of size 2 to receive results from both goroutines.
	results := make(chan hedgeResult[T], 2)

	// Start primary call with a cancellable context.
	primaryCtx, primaryCancel := context.WithCancel(ctx)
	defer primaryCancel()

	go func() {
		v, err := fn(primaryCtx)
		results <- hedgeResult[T]{val: v, err: err, isPrimary: true}
	}()

	// Start a timer for the hedge delay.
	timer := params.Clock.NewTimer(params.Delay)

	// Wait for primary completion, timer, or context cancellation.
	select {
	case result := <-results:
		// Primary completed before delay elapsed.
		timer.Stop()

		if result.err != nil {
			return result.val, result.err
		}

		return result.val, nil

	case <-timer.C():
		// Delay elapsed; primary is still running. Fire hedge.
		params.Hooks.emitHedgeTriggered()

		hedgeCtx, hedgeCancel := context.WithCancel(ctx)
		defer hedgeCancel()

		go func() {
			v, err := fn(hedgeCtx)
			results <- hedgeResult[T]{val: v, err: err, isPrimary: false}
		}()

		// Now wait for first completion from either goroutine.
		//nolint:wrapcheck // internal delegation
		return waitForResults(
			ctx,
			results,
			primaryCancel,
			hedgeCancel,
			params.Hooks,
		)

	case <-ctx.Done():
		timer.Stop()

		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}
}

// waitForResults waits for results from both the primary and hedge goroutines
// after the hedge has been triggered. It returns the first successful result,
// or an error if both fail.
//
//nolint:ireturn,revive // generic type parameter T; argument count justified
// for internal use.
func waitForResults[T any](
	ctx context.Context,
	results chan hedgeResult[T],
	primaryCancel, hedgeCancel context.CancelFunc,
	hooks *Hooks,
) (T, error) {
	var zero T

	// Wait for the first result.
	select {
	case result := <-results:
		if result.err == nil {
			// Success: cancel the loser.
			if result.isPrimary {
				hedgeCancel()
			} else {
				primaryCancel()
				hooks.emitHedgeWon()
			}

			return result.val, nil
		}

		// First result was an error. Wait for the second.
		select {
		case r2 := <-results:
			if r2.err == nil {
				// Second attempt succeeded.
				if r2.isPrimary {
					hedgeCancel()
				} else {
					primaryCancel()
					hooks.emitHedgeWon()
				}

				return r2.val, nil
			}
			// Both failed. Return the first error received.
			return zero, result.err

		case <-ctx.Done():
			return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
		}

	case <-ctx.Done():
		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}
}
