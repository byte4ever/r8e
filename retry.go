package r8e

import (
	"context"
	"fmt"
	"time"
)

// retryConfig holds the optional configuration for retry behavior.
type retryConfig struct {
	maxDelay          time.Duration    // 0 means no cap
	perAttemptTimeout time.Duration    // 0 means no per-attempt timeout
	retryIf           func(error) bool // nil means use default Transient/Permanent check
}

// RetryOption configures retry behavior.
type RetryOption func(*retryConfig)

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

// RetryIf sets a custom predicate that determines whether an error is retryable,
// in addition to the Transient/Permanent classification.
func RetryIf(fn func(error) bool) RetryOption {
	return func(cfg *retryConfig) {
		cfg.retryIf = fn
	}
}

// Pattern: Retry with Backoff — masks transient failures with configurable
// backoff strategy; respects Permanent error classification to stop early.

// DoRetry executes fn with retry logic. It retries up to maxAttempts times using
// the given BackoffStrategy. It respects Transient/Permanent error classification.
func DoRetry[T any](ctx context.Context, maxAttempts int, strategy BackoffStrategy, fn func(context.Context) (T, error), hooks *Hooks, clock Clock, opts ...RetryOption) (T, error) {
	var cfg retryConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	// When maxAttempts is 0 or 1, execute exactly once.
	if maxAttempts <= 1 {
		maxAttempts = 1
	}

	var zero T
	var lastErr error

	for attempt := range maxAttempts {
		// Execute fn, optionally with per-attempt timeout.
		var result T
		var err error
		if cfg.perAttemptTimeout > 0 {
			attemptCtx, attemptCancel := context.WithTimeout(ctx, cfg.perAttemptTimeout)
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
			return zero, err
		}

		// If retryIf predicate is set and returns false: stop.
		if cfg.retryIf != nil && !cfg.retryIf(err) {
			return zero, err
		}

		// If this is the last attempt, don't sleep or emit hook.
		if attempt == maxAttempts-1 {
			break
		}

		// Emit OnRetry hook with 1-indexed attempt number.
		hooks.emitRetry(attempt+1, err)

		// Compute backoff delay.
		delay := strategy.Delay(attempt)

		// Apply MaxDelay cap.
		if cfg.maxDelay > 0 && delay > cfg.maxDelay {
			delay = cfg.maxDelay
		}

		// Sleep using Clock.NewTimer, respecting context cancellation.
		timer := clock.NewTimer(delay)
		select {
		case <-timer.C():
			// Timer fired, proceed to next attempt.
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		}
	}

	// All attempts exhausted: wrap last error with ErrRetriesExhausted.
	return zero, fmt.Errorf("%w: %w", ErrRetriesExhausted, lastErr)
}
