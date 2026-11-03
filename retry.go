package r8e

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type (
	// retryConfig holds the optional configuration for retry behavior.
	retryConfig struct {
		retryIf           func(error) bool
		maxDelay          time.Duration
		perAttemptTimeout time.Duration
		honorRetryAfter   bool
	}

	// RetryOption configures retry behavior.
	//
	// Pattern: Functional Options — composable optional settings (MaxDelay,
	// HonorRetryAfter, PerAttemptTimeout, RetryIf) applied to the private config, keeping the
	// retry call signature stable as options are added.
	RetryOption func(*retryConfig)

	// RetryParams holds the required configuration for retry behavior.
	RetryParams struct {
		Strategy    BackoffStrategy
		Hooks       *Hooks
		Clock       Clock
		Budget      *RetryBudget
		Concurrency *ConcurrencyBudget
		Opts        []RetryOption
		MaxAttempts int
	}
)

// MaxDelay caps the backoff delay to a maximum value. A Retry-After hint is
// capped too, then spread upward by up to 10% so that capped clients do not
// retry together: a capped hint waits between d and 1.1×d.
func MaxDelay(d time.Duration) RetryOption {
	return func(cfg *retryConfig) {
		cfg.maxDelay = d
	}
}

// HonorRetryAfter makes the retry stop, rather than retry early, when a
// server's Retry-After hint exceeds [MaxDelay]. Without it, the hint is capped
// by MaxDelay and the retry leaves before the time the server asked for. With
// it, the retry returns the last error as is, so the caller still reads the
// full hint through its [RetryAfterProvider]; the failure still counts
// against a retry budget. This holds on every attempt, the last one included:
// an over-cap hint is never wrapped with [ErrRetriesExhausted]. The raw hint is
// compared, before any jitter. Without MaxDelay (or with a non-positive one)
// every hint is waited, so HonorRetryAfter has no effect.
func HonorRetryAfter() RetryOption {
	return func(cfg *retryConfig) {
		cfg.honorRetryAfter = true
	}
}

// PerAttemptTimeout sets a timeout for each individual retry attempt. When that
// deadline cuts a failed attempt, the attempt's error is wrapped with
// [ErrAttemptTimeout] and [Hooks.OnAttemptTimeout] fires. The deadline binds
// only when it is earlier than the caller's and than any enclosing r8e
// deadline; otherwise the attempt ends on that other deadline and carries no
// ErrAttemptTimeout.
//
// The cut is decided when the deadline fires, as for [WithTimeout] and a time
// budget: a caller that cancels later, or whose later deadline passes, while
// the cut attempt is still returning does not take it back. A caller that
// cancels before the deadline keeps the attempt. An attempt that returns its
// own error on the deadline, before its context reports it, counts as cut.
//
// The wrap keeps the attempt's own error in the chain and adds nothing to it,
// so errors.Is(err, context.DeadlineExceeded) holds only when that error
// matches context.DeadlineExceeded itself (the context's error does, and so
// does net/http's response-header timeout). Detect the cut with
// errors.Is(err, ErrAttemptTimeout). A cut attempt marked [Permanent], or
// rejected by [RetryIf], is wrapped too and still stops the retry.
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
		// A retry attempt (attempt > 0) must claim a concurrency-budget permit
		// before it runs, so a burst of simultaneous retries cannot pile load
		// onto a struggling downstream. The first attempt is the baseline and is
		// never gated. The permit is checked at execution time — after any
		// backoff — so it reflects the live concurrency, and is held only for the
		// duration of the call so the budget measures concurrent in-flight
		// retries, not backoff waits.
		if attempt > 0 && !params.Concurrency.tryAcquire() {
			params.Hooks.emitConcurrencyBudgetExceeded()

			return zero, fmt.Errorf("%w: %w", ErrConcurrencyBudgetExceeded, lastErr)
		}

		// A non-nil permit is released when the attempt returns — including on a
		// panic unwind — so a panicking fn (with no WithRecover) cannot leak it.
		// Only retries (attempt > 0) hold one; the first attempt acquired none.
		var permit *ConcurrencyBudget
		if attempt > 0 {
			permit = params.Concurrency
		}

		result, cut, err := runRetryAttempt(ctx, fn, cfg, permit)
		if cut {
			params.Hooks.emitAttemptTimeout(attempt + 1)
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
			return zero, err //nolint:wrapcheck // caller's error, wrapped with ErrAttemptTimeout when cut
		}

		// If retryIf predicate is set and returns false: stop (non-retryable).
		if cfg.retryIf != nil && !cfg.retryIf(err) {
			return zero, err //nolint:wrapcheck // caller's error, wrapped with ErrAttemptTimeout when cut
		}

		// Retryable failure: charge it against the retry budget. The terminal
		// attempt is charged too — it is a real downstream failure and a
		// storm contributor — even though no retry follows it.
		params.Budget.recordFailure()

		// A server hint past MaxDelay would be capped and retried early: with
		// HonorRetryAfter, stop and hand the caller the error that carries it.
		// The check runs before the last-attempt exit, so the error comes back
		// as is on every attempt, never wrapped with ErrRetriesExhausted.
		if cfg.honorRetryAfter && hintExceedsMaxDelay(err, cfg.maxDelay) {
			return zero, err
		}

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

		// Compute the wait before the next attempt: the strategy backoff or a
		// Retry-After hint, capped by MaxDelay; a hint is then spread upward
		// by up to 10%, so it can wait past MaxDelay.
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
			// A context that has already ended ended the call: return that
			// end, not the budget's. The time-budget middleware attributes a
			// deadline that is the budget's own (see budgetDeadlineFired), and
			// a caller's deadline the context has reported stays the caller's.
			// A caller's deadline that has passed but that the context has not
			// reported yet (the caller's timer, or the budget's watcher, has
			// not run) reads as live here, so the gate refuses the attempt as
			// below.
			if ctxErr := ctx.Err(); ctxErr != nil {
				return zero, ctxErr //nolint:wrapcheck // preserving context error identity
			}

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

// runRetryAttempt executes one attempt of fn, optionally under a per-attempt
// timeout, and releases the concurrency-budget permit (when permit is non-nil)
// as the attempt returns. The release is deferred so it runs even if fn panics,
// keeping the permit accounting balanced without a WithRecover boundary. When
// the per-attempt deadline cut a failed attempt (see attemptDeadlineFired), cut
// is true and err is fn's error wrapped with ErrAttemptTimeout; otherwise err is
// fn's error unchanged.
//
//nolint:ireturn // generic type parameter T, not an interface
func runRetryAttempt[T any](
	ctx context.Context,
	fn func(context.Context) (T, error),
	cfg retryConfig,
	permit *ConcurrencyBudget,
) (result T, cut bool, err error) {
	if permit != nil {
		defer permit.release()
	}

	if cfg.perAttemptTimeout <= 0 {
		result, err = fn(ctx)

		return result, false, err
	}

	attemptCtx, attemptCancel := withPolicyTimeout(ctx, cfg.perAttemptTimeout)
	defer attemptCancel()

	result, err = fn(attemptCtx)
	if err != nil && attemptDeadlineFired(ctx, attemptCtx) {
		return result, true, fmt.Errorf("%w: %w", ErrAttemptTimeout, err)
	}

	return result, false, err
}

// attemptDeadlineFired reports whether the deadline [PerAttemptTimeout] set on
// attemptCtx cut the attempt. The deadline must bind (see deadlineBinds): one
// that does not bind leaves attemptCtx with parent's deadline. The decision is
// then the first end attemptCtx records, as DoTimeout and the time budget
// decide when their deadline fires: its own deadline means a cut, and a
// parent cancelled before it means none, whatever happens to parent while the
// attempt is still returning. A binding deadline comes strictly before any
// deadline of parent, so DeadlineExceeded as the first end is the attempt's
// own. When attemptCtx has not reported an end yet, the clock decides: an
// attempt that returns on or after its deadline, before the context's timer
// ran, is cut unless parent was cancelled.
func attemptDeadlineFired(parent, attemptCtx context.Context) bool {
	deadline, ok := attemptCtx.Deadline()
	if !ok || !deadlineBinds(parent, deadline) {
		return false
	}

	if err := attemptCtx.Err(); err != nil {
		return errors.Is(err, context.DeadlineExceeded)
	}

	if time.Now().Before(deadline) {
		return false
	}

	err := parent.Err()

	return err == nil || errors.Is(err, context.DeadlineExceeded)
}

// hintExceedsMaxDelay reports whether err carries a Retry-After hint, before
// jitter, longer than maxDelay. A non-positive maxDelay sets no cap, so no hint
// exceeds it.
func hintExceedsMaxDelay(err error, maxDelay time.Duration) bool {
	if maxDelay <= 0 {
		return false
	}

	after, ok := retryAfterFromError(err)

	return ok && after > maxDelay
}

// nextBackoffDelay computes the wait before the next retry attempt: the
// strategy's backoff for this attempt, capped by maxDelay. A server-supplied
// Retry-After hint replaces the backoff when the error carries one: it is
// capped by maxDelay first, then spread upward by up to 10%, so clients capped
// at the same maxDelay do not all retry at the same instant. A capped hint can
// therefore wait up to 10% past maxDelay. A non-positive maxDelay disables the
// cap.
func nextBackoffDelay(
	attempt int,
	err error,
	strategy BackoffStrategy,
	maxDelay time.Duration,
) time.Duration {
	delay := strategy.Delay(attempt)

	after, hinted := retryAfterFromError(err)
	if hinted {
		delay = after
	}

	if maxDelay > 0 && delay > maxDelay {
		delay = maxDelay
	}

	if hinted {
		delay = jitteredRetryAfter(delay)
	}

	return delay
}
