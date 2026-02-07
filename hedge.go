package r8e

import (
	"context"
	"time"
)

// Pattern: Hedged Request — after a delay, fire a second concurrent attempt.
// The first response wins; the other is cancelled. This reduces tail latency
// by racing redundant requests.

// hedgeResult holds the outcome of a hedged call attempt.
type hedgeResult[T any] struct {
	val       T
	err       error
	isPrimary bool
}

// DoHedge executes fn and, if it hasn't completed after delay, fires a second
// concurrent attempt. The first response wins; the other is cancelled.
func DoHedge[T any](ctx context.Context, delay time.Duration, fn func(context.Context) (T, error), hooks *Hooks, clock Clock) (T, error) {
	var zero T

	// If the parent context is already done, return its error immediately.
	if ctx.Err() != nil {
		return zero, ctx.Err()
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
	timer := clock.NewTimer(delay)

	// Wait for primary completion, timer, or context cancellation.
	select {
	case r := <-results:
		// Primary completed before delay elapsed.
		timer.Stop()
		if r.err != nil {
			return r.val, r.err
		}
		return r.val, nil

	case <-timer.C():
		// Delay elapsed; primary is still running. Fire hedge.
		hooks.emitHedgeTriggered()

		hedgeCtx, hedgeCancel := context.WithCancel(ctx)
		defer hedgeCancel()

		go func() {
			v, err := fn(hedgeCtx)
			results <- hedgeResult[T]{val: v, err: err, isPrimary: false}
		}()

		// Now wait for first completion from either goroutine.
		return waitForResults(ctx, results, primaryCancel, hedgeCancel, hooks)

	case <-ctx.Done():
		timer.Stop()
		return zero, ctx.Err()
	}
}

// waitForResults waits for results from both the primary and hedge goroutines
// after the hedge has been triggered. It returns the first successful result,
// or an error if both fail.
func waitForResults[T any](
	ctx context.Context,
	results chan hedgeResult[T],
	primaryCancel context.CancelFunc,
	hedgeCancel context.CancelFunc,
	hooks *Hooks,
) (T, error) {
	var zero T

	// Wait for the first result.
	select {
	case r := <-results:
		if r.err == nil {
			// Success: cancel the loser.
			if r.isPrimary {
				hedgeCancel()
			} else {
				primaryCancel()
				hooks.emitHedgeWon()
			}
			return r.val, nil
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
			return zero, r.err

		case <-ctx.Done():
			return zero, ctx.Err()
		}

	case <-ctx.Done():
		return zero, ctx.Err()
	}
}
