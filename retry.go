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
	//
	// Pattern: Functional Options — composable optional settings (MaxDelay,
	// PerAttemptTimeout, RetryIf) applied to the private config, keeping the
	// retry call signature stable as options are added.
	RetryOption func(*retryConfig)

	// RetryParams holds the required configuration for retry behavior.
	RetryParams struct {
		Strategy    BackoffStrategy
		Hooks       *Hooks
		Clock       Clock
		Budget      *RetryBudget
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
	params RetryParams, //nolint:gocritic // by-value keeps exported signature stable
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

		// On success: credit the retry budget and return immediately.
		if err == nil {
			params.Budget.recordSuccess()

			return result, nil
		}

		lastErr = err

		// If error is Permanent: stop immediately. A non-retryable failure
		// leaves the budget untouched — it cannot drive a retry storm.
		if IsPermanent(err) {
			return zero, err //nolint:wrapcheck // caller's error returned as-is
		}

		// If retryIf predicate is set and returns false: stop (non-retryable).
		if cfg.retryIf != nil && !cfg.retryIf(err) {
			return zero, err //nolint:wrapcheck // caller's error returned as-is
		}

		// Retryable failure: charge it against the retry budget. The terminal
		// attempt is charged too — it is a real downstream failure and a
		// storm contributor — even though no retry follows it.
		params.Budget.recordFailure()

		// If this is the last attempt, don't sleep or emit hook.
		if attempt == maxAttempts-1 {
			break
		}

		// If the budget is exhausted, stop retrying and return the real
		// downstream error; the suppression is observable via the
		// OnRetryBudgetExceeded hook and metrics, not the error value.
		if !params.Budget.allowRetry() {
			params.Hooks.emitRetryBudgetExceeded()

			return zero, lastErr //nolint:wrapcheck // real downstream error
		}

		// Compute the wait before the next attempt: strategy backoff, a
		// Retry-After override, then the MaxDelay cap.
		delay := nextBackoffDelay(attempt, err, params.Strategy, cfg.maxDelay)

		// Honor a total time budget: stop early rather than sleep a backoff that
		// would exhaust the remaining budget and launch an attempt that cannot
		// finish in time. delay >= remaining also covers an already-spent budget
		// (remaining <= 0). The suppression is observable via the
		// OnTimeBudgetExceeded hook and metric. Unlike the retry-budget
		// suppression above (which returns the bare downstream error), this wraps
		// a matchable ErrTimeBudgetExceeded sentinel, since a budget-exhausted
		// deadline is a distinct outcome a caller may want to branch on.
		if remaining, ok := timeBudgetRemaining(ctx, params.Clock); ok && delay >= remaining {
			params.Hooks.emitTimeBudgetExceeded()

			return zero, fmt.Errorf("%w: %w", ErrTimeBudgetExceeded, lastErr)
		}

		// Emit OnRetry hook with 1-indexed attempt number.
		params.Hooks.emitRetry(attempt+1, err)

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

// nextBackoffDelay computes the wait before the next retry attempt: the
// strategy's backoff for this attempt, overridden by a server-supplied
// Retry-After hint (with ±10% jitter to avoid a thundering herd) when the error
// carries one, then capped by maxDelay (which also bounds an over-large
// Retry-After). A non-positive maxDelay disables the cap.
func nextBackoffDelay(
	attempt int,
	err error,
	strategy BackoffStrategy,
	maxDelay time.Duration,
) time.Duration {
	delay := strategy.Delay(attempt)

	if after, ok := retryAfterFromError(err); ok {
		delay = jitteredRetryAfter(after)
	}

	if maxDelay > 0 && delay > maxDelay {
		delay = maxDelay
	}

	return delay
}
