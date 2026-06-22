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
	// ErrBulkheadFull is returned when the bulkhead has no available capacity and
	// the call is rejected immediately — either no max-wait is configured or the
	// bounded wait queue is already at its depth (see [BulkheadMaxWait]).
	ErrBulkheadFull error = resilienceError("bulkhead full")
	// ErrBulkheadTimeout is returned when a call waited the full [BulkheadMaxWait]
	// for a slot without one becoming available. It is distinct from
	// [ErrBulkheadFull] (an immediate rejection) so callers can tell a shed-on-
	// arrival apart from a shed-after-waiting.
	ErrBulkheadTimeout error = resilienceError("bulkhead wait timeout")
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
	// ErrDeadlinePropagationWithoutBudget indicates deadline propagation was
	// requested (a propagate_deadline config field) on a policy with no time
	// budget to derive the deadline from. The code API cannot express this — the
	// flag is an option of [WithTimeBudget] — so it surfaces only from config: it
	// is the error [BuildOptions] and [Policy.Reconfigure] return for that
	// misconfiguration. See [PropagateDeadline].
	ErrDeadlinePropagationWithoutBudget error = resilienceError(
		"deadline propagation requires a time budget",
	)
	// ErrSlowCallConfigIncomplete indicates a [CircuitBreakerConfig] set only one
	// of slow_call_duration / slow_call_rate_threshold. Both are required to
	// enable slow-call-rate tripping (see [SlowCallRate]); supplying one alone is
	// ambiguous. It is the error [BuildOptions] and [Policy.Reconfigure] return
	// for that misconfiguration.
	ErrSlowCallConfigIncomplete error = resilienceError(
		"circuit breaker slow_call_duration and slow_call_rate_threshold " +
			"must be set together",
	)
	// ErrBulkheadWaitWithoutBulkhead indicates a [PolicyConfig] set
	// bulkhead_max_wait or bulkhead_queue_depth without bulkhead; the wait
	// settings have no bulkhead to apply to. It is the error [BuildOptions]
	// returns for that misconfiguration.
	ErrBulkheadWaitWithoutBulkhead error = resilienceError(
		"bulkhead wait settings require a bulkhead",
	)
	// ErrBulkheadQueueWithoutWait indicates a [PolicyConfig] set
	// bulkhead_queue_depth without bulkhead_max_wait; the queue is only used
	// while waiting. It is the error [BuildOptions] returns for that
	// misconfiguration.
	ErrBulkheadQueueWithoutWait error = resilienceError(
		"bulkhead_queue_depth requires bulkhead_max_wait",
	)
	// ErrAIMDWithoutRateLimit indicates AIMD adaptation was requested where it is
	// not available: an aimd config block without rate_limit, a [Policy.Reconfigure]
	// or [RateLimiter.ReconfigureAIMD] targeting a rate limiter that was not built
	// with the [AIMD] option. AIMD cannot be enabled after construction, so the
	// remedy is always to build the rate limiter with WithRateLimit(rate,
	// AIMD(...)). It is the error [BuildOptions], [Policy.Reconfigure], and
	// ReconfigureAIMD return for that misconfiguration.
	ErrAIMDWithoutRateLimit error = resilienceError(
		"AIMD adaptation requires a rate limiter built with the AIMD option",
	)
	// ErrRetryMaxAttemptsRequired indicates a [RetryConfig] omitted max_attempts.
	// It is required: without it the retry would silently collapse to a single
	// attempt. It is the error [BuildOptions] and [Policy.Reconfigure] return for
	// that misconfiguration.
	ErrRetryMaxAttemptsRequired error = resilienceError(
		"retry max_attempts is required",
	)
	// ErrPanic is matched by errors.Is when a panic was recovered by [WithRecover].
	// To inspect the original panic value and goroutine stack trace, use errors.As
	// to obtain the underlying *[PanicError].
	ErrPanic error = resilienceError("panic recovered")
	// ErrConcurrencyBudgetExceeded is returned (wrapping the last downstream
	// error) when a retry is suppressed because the concurrency budget is at its
	// ceiling — too many retries/hedges are already in flight relative to live
	// traffic (see [WithConcurrencyBudget]). An over-budget hedge is silently not
	// launched instead of surfacing this error, since the primary still runs.
	ErrConcurrencyBudgetExceeded error = resilienceError(
		"concurrency budget exceeded",
	)
	// ErrConcurrencyBudgetWithoutConsumer indicates a concurrency budget was
	// configured on a policy with neither [WithRetry] nor [WithHedge]. The budget
	// only gates those two patterns, so without one it would silently do nothing.
	// It is the value [NewPolicy] panics with, and the error [BuildOptions]
	// returns, for that misconfiguration.
	ErrConcurrencyBudgetWithoutConsumer error = resilienceError(
		"concurrency budget requires a retry or hedge pattern to gate",
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
