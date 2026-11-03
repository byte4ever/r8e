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

	// resilienceError is the concrete type backing all sentinel errors. code
	// is the short, stable label [ErrorCode] returns; msg is the message. An
	// envelope sentinel wraps the error that ended the last attempt, and
	// ErrorCode reports that cause before it.
	resilienceError struct {
		code     string
		msg      string
		envelope bool
	}
)

// Sentinel resilience errors.
var (
	// ErrCircuitOpen is returned when the circuit breaker is in the open state.
	ErrCircuitOpen error = resilienceError{code: "circuit_open", msg: "circuit breaker is open"}
	// ErrCircuitRamping is returned when the circuit breaker, recovering through
	// slow-start ramp recovery (see [RampRecovery]), sheds a call because it is
	// still admitting only a fraction of traffic. It is distinct from
	// [ErrCircuitOpen] so callers can tell a partial ramp shed apart from a fully
	// open breaker.
	ErrCircuitRamping error = resilienceError{code: "circuit_ramping", msg: "circuit breaker ramping"}
	// ErrRateLimited is returned when a request is rejected by a rate limiter.
	ErrRateLimited error = resilienceError{code: "rate_limited", msg: "rate limited"}
	// ErrBulkheadFull is returned when the bulkhead has no available capacity and
	// the call is rejected immediately — either no max-wait is configured or the
	// bounded wait queue is already at its depth (see [BulkheadMaxWait]).
	ErrBulkheadFull error = resilienceError{code: "bulkhead_full", msg: "bulkhead full"}
	// ErrBulkheadTimeout is returned when a call waited the full [BulkheadMaxWait]
	// for a slot without one becoming available. It is distinct from
	// [ErrBulkheadFull] (an immediate rejection) so callers can tell a shed-on-
	// arrival apart from a shed-after-waiting.
	ErrBulkheadTimeout error = resilienceError{code: "bulkhead_timeout", msg: "bulkhead wait timeout"}
	// ErrCoDelShed is returned when the bulkhead's controlled-delay discipline
	// (see [BulkheadCoDel]) sheds a queued caller because the wait queue was
	// overloaded — its standing delay stayed above target for a full interval —
	// and the caller had already waited past the slough timeout. It is distinct
	// from [ErrBulkheadTimeout] (a fixed max-wait elapsing) so callers can tell an
	// adaptive controlled-delay drop apart from a deadline timeout.
	ErrCoDelShed error = resilienceError{code: "codel_shed", msg: "shed by controlled-delay queue"}
	// ErrConcurrencyLimited is returned when the adaptive concurrency limiter is
	// at its current limit and rejects a call.
	ErrConcurrencyLimited error = resilienceError{code: "concurrency_limited", msg: "concurrency limited"}
	// ErrCoolingDown is returned when a call arrives while the policy's
	// cooldown is armed: a server asked to wait (a Retry-After hint) and the
	// deadline has not passed (see [WithCooldown]). It is a local rejection
	// (see [IsLocalRejection]).
	ErrCoolingDown error = resilienceError{code: "cooling_down", msg: "cooling down"}
	// ErrCooldownNonPositiveMax is the value [NewCooldown] and [NewPolicy]
	// panic with when [MaxCooldown] is not positive, and the error
	// [BuildOptions], [Policy.Reconfigure] and [Cooldown.SetMaxCooldown]
	// return for a non-positive cap.
	ErrCooldownNonPositiveMax error = resilienceError{
		code: "cooldown_non_positive_max",
		msg:  "cooldown cap must be positive",
	}
	// ErrSharedCooldownConflict indicates a policy was given both
	// [WithCooldown] and [WithSharedCooldown], the value [NewPolicy] panics
	// with; or that Policy.Reconfigure was asked to change the cap of a shared
	// cooldown, which only [Cooldown.SetMaxCooldown] does.
	ErrSharedCooldownConflict error = resilienceError{
		code: "shared_cooldown_conflict",
		msg:  "a shared cooldown excludes a policy cooldown",
	}
	// ErrThrottled is returned when the adaptive throttler sheds a call locally
	// to protect a struggling backend (see [WithAdaptiveThrottle]).
	ErrThrottled error = resilienceError{code: "throttled", msg: "adaptively throttled"}
	// ErrSLOShed is returned when the SLO burn-rate governor sheds a call locally
	// because the service-level objective's error budget is burning too fast (see
	// [WithSLO]). It is distinct from [ErrThrottled] so callers can tell a
	// budget-protection shed apart from a backend-health shed.
	ErrSLOShed error = resilienceError{code: "slo_shed", msg: "shed to protect the SLO error budget"}
	// ErrTimeout is returned when an operation exceeds its deadline.
	ErrTimeout error = resilienceError{code: "timeout", msg: "timeout"}
	// ErrTimeBudgetExceeded is returned (wrapping the last downstream error) when
	// retry stops early because the total time budget would be exhausted by the
	// next backoff. See [WithTimeBudget].
	ErrTimeBudgetExceeded error = resilienceError{
		code:     "time_budget_exceeded",
		msg:      "time budget exceeded",
		envelope: true,
	}
	// ErrAttemptTimeout wraps the error of a retry attempt that the attempt
	// deadline set by [PerAttemptTimeout] cut. It keeps the attempt's own error
	// in the chain and adds nothing to it, so errors.Is(err,
	// context.DeadlineExceeded) holds only when that error matches
	// context.DeadlineExceeded itself: the context's error does, and so does a
	// downstream timeout error that matches it, such as net/http's
	// response-header timeout. An attempt ended first by the caller's
	// deadline or cancellation, a time budget or [WithTimeout] does not carry
	// it. It is not an envelope: [ErrorCode] reports "attempt_timeout" for it,
	// ahead of any r8e sentinel in the attempt's own error.
	ErrAttemptTimeout error = resilienceError{code: "attempt_timeout", msg: "attempt timeout"}
	// ErrRetriesExhausted is returned when all retry attempts have been used.
	ErrRetriesExhausted error = resilienceError{
		code:     "retries_exhausted",
		msg:      "retries exhausted",
		envelope: true,
	}
	// ErrRetryBudgetWithoutRetry indicates a retry budget was configured on a
	// policy that has no retry pattern; the budget would have nothing to gate.
	// It is the value [NewPolicy] panics with and the error [BuildOptions]
	// returns for the same misconfiguration sourced from config.
	ErrRetryBudgetWithoutRetry error = resilienceError{
		code: "retry_budget_without_retry",
		msg:  "retry budget requires a retry pattern",
	}
	// ErrCoalesceNilKeyFunc indicates [WithCoalesce] was given a nil key
	// function; coalescing has no way to group calls without one. It is the
	// value [NewPolicy] panics with for that misconfiguration.
	ErrCoalesceNilKeyFunc error = resilienceError{
		code: "coalesce_nil_key_func",
		msg:  "coalesce requires a non-nil key function",
	}
	// ErrCoalesceWithoutTimeout indicates [WithCoalesce] was configured on a
	// policy with no [WithTimeout]. The coalesced call runs under a context
	// detached from its callers, so without a timeout to bound it a leader whose
	// fn never returns would park a goroutine and wedge its key indefinitely. It
	// is the value [NewPolicy] panics with for that misconfiguration.
	ErrCoalesceWithoutTimeout error = resilienceError{
		code: "coalesce_without_timeout",
		msg:  "coalesce requires a timeout to bound the detached shared call",
	}
	// ErrCacheNilKeyFunc indicates [WithCache] was given a nil key function;
	// the cache has no way to derive a key per call without one. It is the value
	// [NewPolicy] panics with for that misconfiguration.
	ErrCacheNilKeyFunc error = resilienceError{
		code: "cache_nil_key_func",
		msg:  "cache requires a non-nil key function",
	}
	// ErrCacheNilCache indicates [WithCache] was given a nil [Cache]; there is
	// nothing to read from or write to. It is the value [NewPolicy] panics with
	// for that misconfiguration.
	ErrCacheNilCache error = resilienceError{
		code: "cache_nil_cache",
		msg:  "cache requires a non-nil cache",
	}
	// ErrCacheNonPositiveTTL indicates [WithCache] was given a non-positive fresh
	// TTL; a zero or negative TTL would make every entry stale on arrival, so the
	// cache could never serve a hit. It is the value [NewPolicy] panics with for
	// that misconfiguration.
	ErrCacheNonPositiveTTL error = resilienceError{
		code: "cache_non_positive_ttl",
		msg:  "cache requires a positive TTL",
	}
	// ErrRefreshAheadWithoutTimeout indicates [WithCache] was configured with a
	// firing [RefreshAhead] (a refresh threshold inside the fresh ttl) but the
	// policy has no [WithTimeout]. The refresh-ahead reload runs in a detached
	// background goroutine whose deadline is stripped, so it needs an inner
	// timeout to bound it (a reload that never returns would park a goroutine and
	// wedge the key's refresh slot). It is the value [NewPolicy] panics with for
	// that misconfiguration. Caching is code-only (absent from [PolicyConfig]), so
	// unlike the cross-pattern config errors this is never returned by
	// [BuildOptions] — the config path cannot express a cache.
	ErrRefreshAheadWithoutTimeout error = resilienceError{
		code: "refresh_ahead_without_timeout",
		msg:  "cache refresh-ahead requires a timeout to bound the detached reload",
	}
	// ErrConcurrencyLimiterConflict indicates a policy was configured with both
	// [WithBulkhead] and [WithAdaptiveConcurrency]. Both drive the same
	// concurrency-limiting slot, so they are mutually exclusive. It is the value
	// [NewPolicy] panics with, and the error [BuildOptions] returns, for that
	// misconfiguration.
	ErrConcurrencyLimiterConflict error = resilienceError{
		code: "concurrency_limiter_conflict",
		msg:  "bulkhead and adaptive concurrency are mutually exclusive",
	}
	// ErrSharedBulkheadConflict indicates a policy built with
	// [WithSharedBulkhead] was also given its own concurrency limiter
	// ([WithBulkhead] or [WithAdaptiveConcurrency]), the value [NewPolicy]
	// panics with; or that [Policy.Reconfigure] was given a bulkhead setting
	// (size, max wait, queue depth or CoDel) for such a policy, which only
	// [Bulkhead.Reconfigure] retunes.
	ErrSharedBulkheadConflict error = resilienceError{
		code: "shared_bulkhead_conflict",
		msg:  "a shared bulkhead excludes the policy's own concurrency limiter and bulkhead settings",
	}
	// ErrTimeBudgetWithoutConsumer indicates [WithTimeBudget] was configured on a
	// policy with neither [WithRetry] nor [WithHedge]. The budget only gates
	// those two patterns, so without one it would silently do nothing. It is the
	// value [NewPolicy] panics with, and the error [BuildOptions] returns, for
	// that misconfiguration.
	ErrTimeBudgetWithoutConsumer error = resilienceError{
		code: "time_budget_without_consumer",
		msg:  "time budget requires a retry or hedge pattern to gate",
	}
	// ErrDeadlinePropagationWithoutBudget indicates deadline propagation was
	// requested (a propagate_deadline config field) on a policy with no time
	// budget to derive the deadline from. The code API cannot express this — the
	// flag is an option of [WithTimeBudget] — so it surfaces only from config: it
	// is the error [BuildOptions] and [Policy.Reconfigure] return for that
	// misconfiguration. See [PropagateDeadline].
	ErrDeadlinePropagationWithoutBudget error = resilienceError{
		code: "deadline_propagation_without_budget",
		msg:  "deadline propagation requires a time budget",
	}
	// ErrInboundDeadlineWithoutBudget indicates inbound-deadline honoring was
	// requested (a respect_inbound_deadline config field) on a policy with no
	// time budget to tighten. The code API cannot express this — the flag is an
	// option of [WithTimeBudget] — so it surfaces only from config: it is the
	// error [BuildOptions] and [Policy.Reconfigure] return for that
	// misconfiguration. See [RespectInboundDeadline].
	ErrInboundDeadlineWithoutBudget error = resilienceError{
		code: "inbound_deadline_without_budget",
		msg:  "inbound deadline honoring requires a time budget",
	}
	// ErrSlowCallConfigIncomplete indicates a [CircuitBreakerConfig] set only one
	// of slow_call_duration / slow_call_rate_threshold. Both are required to
	// enable slow-call-rate tripping (see [SlowCallRate]); supplying one alone is
	// ambiguous. It is the error [BuildOptions] and [Policy.Reconfigure] return
	// for that misconfiguration.
	ErrSlowCallConfigIncomplete error = resilienceError{
		code: "slow_call_config_incomplete",
		msg: "circuit breaker slow_call_duration and slow_call_rate_threshold " +
			"must be set together",
	}
	// ErrBulkheadWaitWithoutBulkhead indicates a [PolicyConfig] set a bulkhead
	// wait setting (bulkhead_max_wait, bulkhead_queue_depth, or one of the
	// bulkhead_codel_* fields) without bulkhead; the wait settings have no
	// bulkhead to apply to. It is the error [BuildOptions] and [Policy.Reconfigure]
	// return for that misconfiguration.
	ErrBulkheadWaitWithoutBulkhead error = resilienceError{
		code: "bulkhead_wait_without_bulkhead",
		msg:  "bulkhead wait settings require a bulkhead",
	}
	// ErrBulkheadQueueWithoutWait indicates a [PolicyConfig] set
	// bulkhead_queue_depth without a wait to bound; the queue is only used while
	// waiting, so it needs either bulkhead_max_wait or the controlled-delay
	// discipline (bulkhead_codel_target/interval) enabled. It is the error
	// [BuildOptions] returns for that misconfiguration.
	ErrBulkheadQueueWithoutWait error = resilienceError{
		code: "bulkhead_queue_without_wait",
		msg:  "bulkhead_queue_depth requires a wait (bulkhead_max_wait or bulkhead_codel)",
	}
	// ErrBulkheadCoDelConfigIncomplete indicates a [PolicyConfig] set only one of
	// bulkhead_codel_target / bulkhead_codel_interval. Both are required to enable
	// the controlled-delay discipline (see [BulkheadCoDel]); supplying one alone is
	// ambiguous. It is the error [BuildOptions] and [Policy.Reconfigure] return for
	// that misconfiguration.
	ErrBulkheadCoDelConfigIncomplete error = resilienceError{
		code: "bulkhead_codel_config_incomplete",
		msg:  "bulkhead_codel_target and bulkhead_codel_interval must be set together",
	}
	// ErrAIMDWithoutRateLimit indicates AIMD adaptation was requested where it is
	// not available: an aimd config block without rate_limit, a [Policy.Reconfigure]
	// or [RateLimiter.ReconfigureAIMD] targeting a rate limiter that was not built
	// with the [AIMD] option. AIMD cannot be enabled after construction, so the
	// remedy is always to build the rate limiter with WithRateLimit(rate,
	// AIMD(...)). It is the error [BuildOptions], [Policy.Reconfigure], and
	// ReconfigureAIMD return for that misconfiguration.
	ErrAIMDWithoutRateLimit error = resilienceError{
		code: "aimd_without_rate_limit",
		msg:  "AIMD adaptation requires a rate limiter built with the AIMD option",
	}
	// ErrBurstWithoutRateLimit indicates a [PolicyConfig] set rate_limit_burst
	// without rate_limit: the burst sizes the rate limiter's bucket, so without
	// a rate limiter it would silently do nothing. It is the error
	// [BuildOptions] returns for that misconfiguration.
	ErrBurstWithoutRateLimit error = resilienceError{
		code: "burst_without_rate_limit",
		msg:  "rate limit burst requires a rate limit",
	}
	// ErrRateLimitBurstOutOfRange indicates a rate limiter burst (see [Burst])
	// below 1 or above [MaxBurst]. It is the value [NewRateLimiter] and
	// [NewPolicy] panic with, and the error [BuildOptions] and
	// [Policy.Reconfigure] return, for that misconfiguration.
	ErrRateLimitBurstOutOfRange error = resilienceError{
		code: "rate_limit_burst_out_of_range",
		msg:  "rate limit burst must be between 1 and MaxBurst",
	}
	// ErrAdaptiveTimeoutWithoutTimeout indicates percentile-driven adaptive timeout
	// was requested where there is no timeout to adapt: an adaptive_timeout config
	// block without timeout, or a [Policy.Reconfigure] targeting a policy whose
	// timeout was not built with the [AdaptiveTimeout] option. Adaptation cannot be
	// enabled after construction, so the remedy is to build the timeout with
	// WithTimeout(d, AdaptiveTimeout(...)). It is the error [BuildOptions] and
	// [Policy.Reconfigure] return for that misconfiguration.
	ErrAdaptiveTimeoutWithoutTimeout error = resilienceError{
		code: "adaptive_timeout_without_timeout",
		msg:  "adaptive timeout requires a timeout built with the AdaptiveTimeout option",
	}
	// ErrAdaptiveHedgeWithoutHedge indicates percentile-driven adaptive hedge delay
	// was requested where there is no hedge to adapt: an adaptive_hedge config block
	// without hedge, or a [Policy.Reconfigure] targeting a policy whose hedge was not
	// built with the [AdaptiveHedge] option. Adaptation cannot be enabled after
	// construction, so the remedy is to build the hedge with
	// WithHedge(d, AdaptiveHedge(...)). It is the error [BuildOptions] and
	// [Policy.Reconfigure] return for that misconfiguration.
	ErrAdaptiveHedgeWithoutHedge error = resilienceError{
		code: "adaptive_hedge_without_hedge",
		msg:  "adaptive hedge requires a hedge built with the AdaptiveHedge option",
	}
	// ErrRetryMaxAttemptsRequired indicates a [RetryConfig] omitted max_attempts.
	// It is required: without it the retry would silently collapse to a single
	// attempt. It is the error [BuildOptions] and [Policy.Reconfigure] return for
	// that misconfiguration.
	ErrRetryMaxAttemptsRequired error = resilienceError{
		code: "retry_max_attempts_required",
		msg:  "retry max_attempts is required",
	}
	// ErrSLOTargetRequired indicates an [SLOConfig] block was supplied without a
	// target success rate. The target has no sensible default (it is the whole
	// objective), so it is required whenever the config requests an SLO governor.
	// It is the error [BuildOptions] and [Policy.Reconfigure] return for that
	// misconfiguration. The code API takes the target positionally
	// ([WithSLO]), so it cannot express this.
	ErrSLOTargetRequired error = resilienceError{
		code: "slo_target_required",
		msg:  "slo target is required",
	}
	// ErrPanic is matched by errors.Is when a panic was recovered by [WithRecover].
	// To inspect the original panic value and goroutine stack trace, use errors.As
	// to obtain the underlying *[PanicError].
	ErrPanic error = resilienceError{code: "panic_recovered", msg: "panic recovered"}
	// ErrConcurrencyBudgetExceeded is returned (wrapping the last downstream
	// error) when a retry is suppressed because the concurrency budget is at its
	// ceiling — too many retries/hedges are already in flight relative to live
	// traffic (see [WithConcurrencyBudget]). An over-budget hedge is silently not
	// launched instead of surfacing this error, since the primary still runs.
	ErrConcurrencyBudgetExceeded error = resilienceError{
		code:     "concurrency_budget_exceeded",
		msg:      "concurrency budget exceeded",
		envelope: true,
	}
	// ErrChaosInjected is the default error a [ChaosFault] strategy injects when
	// the caller passes a nil error (see [WithChaos]). Match it with errors.Is to
	// tell a chaos-injected failure apart from a real downstream one.
	ErrChaosInjected error = resilienceError{code: "chaos_injected", msg: "chaos fault injected"}
	// ErrConcurrencyBudgetWithoutConsumer indicates a concurrency budget was
	// configured on a policy with neither [WithRetry] nor [WithHedge]. The budget
	// only gates those two patterns, so without one it would silently do nothing.
	// It is the value [NewPolicy] panics with, and the error [BuildOptions]
	// returns, for that misconfiguration.
	ErrConcurrencyBudgetWithoutConsumer error = resilienceError{
		code: "concurrency_budget_without_consumer",
		msg:  "concurrency budget requires a retry or hedge pattern to gate",
	}
)

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func (e *permanentError) Error() string { return e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func (e resilienceError) Error() string { return e.msg }

// ErrorCode returns the short, stable code of the r8e sentinel that err
// carries, such as "circuit_open" or "panic_recovered", for logs, metrics and
// trace labels. It walks err's tree as [errors.As] does, outermost first, and
// returns the first sentinel it finds that is not an envelope. The envelopes
// ([ErrRetriesExhausted], [ErrTimeBudgetExceeded] and
// [ErrConcurrencyBudgetExceeded]) wrap the error that ended the last attempt,
// so they yield to the cause they carry: a circuit breaker rejection, a panic
// or a chaos fault wrapped in ErrRetriesExhausted reports its own code. An
// error that carries only envelopes reports the outermost one. ErrorCode
// returns "" when err carries no r8e sentinel, nil included.
//
// ErrorCode reaches a sentinel through Unwrap, never through an Is method. An
// error type of your own that matches a sentinel only through its Is method
// carries no code: wrap the sentinel with %w instead.
func ErrorCode(err error) string {
	outermost := ""
	cause := ""

	walkSentinels(err, func(sentinel resilienceError) bool {
		if !sentinel.envelope {
			cause = sentinel.code

			return true
		}

		if outermost == "" {
			outermost = sentinel.code
		}

		return false
	})

	if cause != "" {
		return cause
	}

	return outermost
}

// walkSentinels calls visit on each r8e sentinel in err's tree, in the
// pre-order [errors.As] uses, until visit returns true. It reports whether
// visit stopped the walk.
func walkSentinels(err error, visit func(resilienceError) bool) bool {
	if err == nil {
		return false
	}

	//nolint:errorlint // walks the tree node by node, as errors.As does
	switch wrapper := err.(type) {
	case resilienceError:
		return visit(wrapper)
	case interface{ Unwrap() error }:
		return walkSentinels(wrapper.Unwrap(), visit)
	case interface{ Unwrap() []error }:
		for _, inner := range wrapper.Unwrap() {
			if walkSentinels(inner, visit) {
				return true
			}
		}

		return false
	default:
		return false
	}
}

// Transient wraps err to mark it as a transient (retriable) error.
// The wrapper's message is err's message, unchanged. The mark states intent
// only: [IsTransient] reports true for every error not marked [Permanent], so
// Transient cannot make a permanent error retriable. Returns nil if err is nil.
func Transient(err error) error {
	if err == nil {
		return nil
	}

	return &transientError{err: err}
}

// Permanent wraps err to mark it as a permanent (non-retriable) error.
// The wrapper's message is err's message, unchanged: the mark is read with
// [IsPermanent], never from the text. Returns nil if err is nil.
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
