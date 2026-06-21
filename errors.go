package r8e

import "errors"

// ---------------------------------------------------------------------------
// Error classification wrappers
// ---------------------------------------------------------------------------.

type (
	// transientError marks a wrapped error as transient (retriable).
	transientError struct {
		err error
	}

	// permanentError marks a wrapped error as permanent (non-retriable).
	permanentError struct {
		err error
	}

	// resilienceError is the concrete type backing all sentinel errors.
	resilienceError string
)

// Sentinel resilience errors.
var (
	// ErrCircuitOpen is returned when the circuit breaker is in the open state.
	ErrCircuitOpen error = resilienceError("circuit breaker is open")
	// ErrRateLimited is returned when a request is rejected by a rate limiter.
	ErrRateLimited error = resilienceError("rate limited")
	// ErrBulkheadFull is returned when the bulkhead has no available capacity.
	ErrBulkheadFull error = resilienceError("bulkhead full")
	// ErrConcurrencyLimited is returned when the adaptive concurrency limiter is
	// at its current limit and rejects a call.
	ErrConcurrencyLimited error = resilienceError("concurrency limited")
	// ErrThrottled is returned when the adaptive throttler sheds a call locally
	// to protect a struggling backend (see [WithAdaptiveThrottle]).
	ErrThrottled error = resilienceError("adaptively throttled")
	// ErrTimeout is returned when an operation exceeds its deadline.
	ErrTimeout error = resilienceError("timeout")
	// ErrTimeBudgetExceeded is returned (wrapping the last downstream error) when
	// retry stops early because the total time budget would be exhausted by the
	// next backoff. See [WithTimeBudget].
	ErrTimeBudgetExceeded error = resilienceError("time budget exceeded")
	// ErrRetriesExhausted is returned when all retry attempts have been used.
	ErrRetriesExhausted error = resilienceError("retries exhausted")
	// ErrRetryBudgetWithoutRetry indicates a retry budget was configured on a
	// policy that has no retry pattern; the budget would have nothing to gate.
	// It is the value [NewPolicy] panics with and the error [BuildOptions]
	// returns for the same misconfiguration sourced from config.
	ErrRetryBudgetWithoutRetry error = resilienceError(
		"retry budget requires a retry pattern",
	)
	// ErrCoalesceNilKeyFunc indicates [WithCoalesce] was given a nil key
	// function; coalescing has no way to group calls without one. It is the
	// value [NewPolicy] panics with for that misconfiguration.
	ErrCoalesceNilKeyFunc error = resilienceError(
		"coalesce requires a non-nil key function",
	)
	// ErrCoalesceWithoutTimeout indicates [WithCoalesce] was configured on a
	// policy with no [WithTimeout]. The coalesced call runs under a context
	// detached from its callers, so without a timeout to bound it a leader whose
	// fn never returns would park a goroutine and wedge its key indefinitely. It
	// is the value [NewPolicy] panics with for that misconfiguration.
	ErrCoalesceWithoutTimeout error = resilienceError(
		"coalesce requires a timeout to bound the detached shared call",
	)
	// ErrCacheNilKeyFunc indicates [WithCache] was given a nil key function;
	// the cache has no way to derive a key per call without one. It is the value
	// [NewPolicy] panics with for that misconfiguration.
	ErrCacheNilKeyFunc error = resilienceError(
		"cache requires a non-nil key function",
	)
	// ErrCacheNilCache indicates [WithCache] was given a nil [Cache]; there is
	// nothing to read from or write to. It is the value [NewPolicy] panics with
	// for that misconfiguration.
	ErrCacheNilCache error = resilienceError(
		"cache requires a non-nil cache",
	)
	// ErrCacheNonPositiveTTL indicates [WithCache] was given a non-positive fresh
	// TTL; a zero or negative TTL would make every entry stale on arrival, so the
	// cache could never serve a hit. It is the value [NewPolicy] panics with for
	// that misconfiguration.
	ErrCacheNonPositiveTTL error = resilienceError(
		"cache requires a positive TTL",
	)
	// ErrConcurrencyLimiterConflict indicates a policy was configured with both
	// [WithBulkhead] and [WithAdaptiveConcurrency]. Both drive the same
	// concurrency-limiting slot, so they are mutually exclusive. It is the value
	// [NewPolicy] panics with, and the error [BuildOptions] returns, for that
	// misconfiguration.
	ErrConcurrencyLimiterConflict error = resilienceError(
		"bulkhead and adaptive concurrency are mutually exclusive",
	)
	// ErrTimeBudgetWithoutConsumer indicates [WithTimeBudget] was configured on a
	// policy with neither [WithRetry] nor [WithHedge]. The budget only gates
	// those two patterns, so without one it would silently do nothing. It is the
	// value [NewPolicy] panics with, and the error [BuildOptions] returns, for
	// that misconfiguration.
	ErrTimeBudgetWithoutConsumer error = resilienceError(
		"time budget requires a retry or hedge pattern to gate",
	)
)

func (e *transientError) Error() string { return "transient: " + e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func (e *permanentError) Error() string { return "permanent: " + e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func (e resilienceError) Error() string { return string(e) }

// Transient wraps err to mark it as a transient (retriable) error.
// Returns nil if err is nil.
func Transient(err error) error {
	if err == nil {
		return nil
	}

	return &transientError{err: err}
}

// Permanent wraps err to mark it as a permanent (non-retriable) error.
// Returns nil if err is nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}

	return &permanentError{err: err}
}

// IsTransient reports whether err is transient. Unclassified (unwrapped)
// errors are treated as transient. Returns false for nil.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	// Explicitly permanent errors are not transient.
	var pe *permanentError

	return !errors.As(err, &pe)
}

// IsPermanent reports whether err was explicitly marked as permanent.
// Returns false for nil and for unclassified errors.
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}

	var pe *permanentError

	return errors.As(err, &pe)
}
