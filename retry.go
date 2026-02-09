package r8e

import (
	"context"
	"fmt"
	"time"
)

type (
	// retryConfig holds the optional configuration for retry behavior.
	retryConfig struct {
		retryIf           func(error) bool
		maxDelay          time.Duration
		perAttemptTimeout time.Duration
	}

	// RetryOption configures retry behavior.
	RetryOption func(*retryConfig)

	// RetryParams holds the required configuration for retry behavior.
	RetryParams struct {
		Strategy    BackoffStrategy
		Hooks       *Hooks
		Clock       Clock
		Opts        []RetryOption
		MaxAttempts int
	}
)

// MaxDelay caps the backoff delay to a maximum value.
func MaxDelay(d time.Duration) RetryOption {
	return func(cfg *retryConfig) {
		cfg.maxDelay = d
	}
}

// PerAttemptTimeout sets a timeout for each individual retry attempt.
func PerAttemptTimeout(d time.Duration) RetryOption {
	return func(cfg *retryConfig) {
		cfg.perAttemptTimeout = d
	}
}

// RetryIf sets a custom predicate that determines whether an error is
// retryable,
// in addition to the Transient/Permanent classification.
func RetryIf(fn func(error) bool) RetryOption {
	return func(cfg *retryConfig) {
		cfg.retryIf = fn
	}
}

// Pattern: Retry with Backoff — masks transient failures with configurable
// backoff strategy; respects Permanent error classification to stop early.

// DoRetry executes fn with retry logic. It retries up to params.MaxAttempts
// times using the given BackoffStrategy. It respects Transient/Permanent error
// classification.
//
//nolint:ireturn // generic type parameter T, not an interface
func DoRetry[T any](
	ctx context.Context,
	fn func(context.Context) (T, error),
	params RetryParams,
) (T, error) {
	var cfg retryConfig
	for _, opt := range params.Opts {
		opt(&cfg)
	}

	// When maxAttempts is 0 or 1, execute exactly once.
	maxAttempts := max(params.MaxAttempts, 1)

	var (
		zero    T
		lastErr error
	)

	for attempt := range maxAttempts {
		// Execute fn, optionally with per-attempt timeout.
		var (
			result T
			err    error
		)

		if cfg.perAttemptTimeout > 0 {
			attemptCtx, attemptCancel := context.WithTimeout(
				ctx,
				cfg.perAttemptTimeout,
			)
			result, err = fn(attemptCtx)

			attemptCancel()
		} else {
			result, err = fn(ctx)
		}

		// On success: return result immediately.
		if err == nil {
			return result, nil
		}

		lastErr = err

		// If error is Permanent: stop immediately.
		if IsPermanent(err) {
			return zero, err //nolint:wrapcheck // caller's error returned as-is
		}

		// If retryIf predicate is set and returns false: stop.
		if cfg.retryIf != nil && !cfg.retryIf(err) {
			return zero, err //nolint:wrapcheck // caller's error returned as-is
		}

		// If this is the last attempt, don't sleep or emit hook.
		if attempt == maxAttempts-1 {
			break
		}

		// Emit OnRetry hook with 1-indexed attempt number.
		params.Hooks.emitRetry(attempt+1, err)

		// Compute backoff delay.
		delay := params.Strategy.Delay(attempt)

		// Apply MaxDelay cap.
		if cfg.maxDelay > 0 && delay > cfg.maxDelay {
			delay = cfg.maxDelay
		}

		// Sleep using Clock.NewTimer, respecting context cancellation.
		timer := params.Clock.NewTimer(delay)
		select {
		case <-timer.C():
			// Timer fired, proceed to next attempt.
		case <-ctx.Done():
			timer.Stop()

			return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
		}
	}

	// All attempts exhausted: wrap last error with ErrRetriesExhausted.
	return zero, fmt.Errorf("%w: %w", ErrRetriesExhausted, lastErr)
}
