package r8e

import (
	"context"
	"errors"
	"time"
)

type (
	// Outcome is what one completed call says about the health of the
	// downstream a [CircuitBreaker] protects. A classifier (see
	// [CircuitBreakerClassifier]) maps each call's error to an Outcome, and a
	// [CircuitPermit] reports it to the breaker.
	//
	// The zero Outcome is none of the named outcomes and counts as a failure,
	// so an outcome left unset fails safe.
	Outcome int

	// CircuitPermit is one admission granted by [CircuitBreaker.Acquire]. It
	// remembers which recovery phase admitted the call and whether the call
	// holds a half-open probe slot, so its outcome only affects the breaker it
	// was admitted into: an outcome reported after the breaker has opened or
	// started a new half-open phase since the admission is ignored.
	//
	// A CircuitPermit is a value. Report its outcome once, through
	// [CircuitPermit.Done] on the variable Acquire returned; a copy keeps its
	// own reported flag, so Done on both the original and a copy reports
	// twice. It is not safe for concurrent use.
	CircuitPermit struct {
		cb         *CircuitBreaker
		epoch      uint64
		holdsProbe bool
		reported   bool
	}
)

// Call outcomes. The zero Outcome is deliberately left out (see [Outcome]).
const (
	// OutcomeSuccess is a call that shows the downstream is healthy.
	OutcomeSuccess Outcome = iota + 1
	// OutcomeFailure is a call that shows the downstream is unhealthy. Any
	// value other than OutcomeSuccess and OutcomeNeutral is treated as a
	// failure.
	OutcomeFailure
	// OutcomeNeutral is a call that says nothing about the downstream's
	// health, such as a call its caller cancelled. It leaves the failure
	// counter and the slow-call window untouched; in the half-open state it
	// frees the probe slot without counting, so the next call can probe right
	// away.
	OutcomeNeutral
)

// CircuitBreakerClassifier sets how the circuit-breaker middleware of a
// [Policy] turns each call into an [Outcome]. The classifier receives the
// call's context and the error returned by the inner chain; the caller still
// receives that error unchanged. A nil classifier restores the default, under
// which a nil error is a success and any other error a failure, exactly as
// before outcomes existed.
//
// The classifier runs outside the breaker's lock, once per admitted call that
// returns; a call that panics is recorded as a failure without calling it. It
// is called concurrently by every goroutine running the policy, so it must be
// safe for concurrent use. When the policy also has [WithTimeout], the
// timeout returns [ErrTimeout] to the caller as soon as it fires, while the
// inner chain keeps running on its own goroutine: the classifier, the
// breaker's recording of the call and any lifecycle hook it triggers can then
// run after [Policy.Do] has returned. [WithCoalesce] and the refresh-ahead of
// [WithCache] also run the chain on a goroutine of their own, detached from
// the caller.
//
// The classifier is code, so it has no counterpart in [CircuitBreakerConfig].
// It applies to the policy middleware only: a standalone caller of
// [CircuitBreaker.Acquire] passes the outcome to [CircuitPermit.Done] itself,
// and [CircuitBreaker.Record] keeps its error-based behaviour.
//
// See [CallerCancellationIsNeutral] for a ready-made classifier, and
// [IsCallerCancellation] and [IsLocalRejection] to build your own. By default
// the breaker also counts the rejections of the rate limiter, bulkhead and
// adaptive concurrency limiter that run inside it (see IsLocalRejection).
func CircuitBreakerClassifier(fn func(context.Context, error) Outcome) CircuitBreakerOption {
	return func(cfg *circuitBreakerConfig) {
		cfg.classifier = fn
	}
}

// IsCallerCancellation reports whether err comes from the caller giving up on
// the call: ctx is done, and err is, or wraps, ctx.Err() or
// [context.Cause](ctx). Matching the cause covers a caller that cancels with
// [context.WithCancelCause], as errgroup does, since net/http returns the
// cause rather than ctx.Err(). A downstream that fails on its own with an
// error equal to that cause, after the caller cancelled, reads the same way.
//
// It reports false when ctx ended on a deadline that r8e set and that was the
// earliest deadline ctx had:
//   - a [WithTimeout] timeout or a [DoTimeout] deadline, of this policy or of
//     an enclosing one;
//   - a per-attempt timeout of an enclosing policy's [WithRetry] (see
//     PerAttemptTimeout);
//   - a [WithTimeBudget] deadline set with [PropagateDeadline] that is earlier
//     than the deadline its context already had.
//
// A time budget that [RespectInboundDeadline] tightened to an existing
// deadline sets none of its own: the call is attributed to whoever set that
// deadline, the caller or an r8e timeout. Any other end of ctx reports true.
// That includes a hedge cancelling its losing attempt in an enclosing policy,
// and a caller deadline set below an r8e deadline that expires later, whenever
// the call returns.
//
// Each r8e deadline that binds records its instant in the context; one that
// does not bind records nothing. ctx counts as ended by r8e only when its
// deadline equals the innermost recorded instant. [context.WithoutCancel]
// keeps the record but drops the deadline, so a caller deadline set under it
// is the caller's unless it falls exactly on the innermost recorded instant.
//
// With [CallerCancellationIsNeutral], a downstream that hangs is therefore
// detected only when an r8e deadline ends the call before the caller gives
// up: keep the policy's [WithTimeout] shorter than the callers' deadlines.
func IsCallerCancellation(ctx context.Context, err error) bool {
	ctxErr := ctx.Err()
	if err == nil || ctxErr == nil {
		return false
	}

	if !errors.Is(err, ctxErr) && !errors.Is(err, context.Cause(ctx)) {
		return false
	}

	return !policyDeadlineEnded(ctx)
}

// CallerCancellationIsNeutral is a classifier for [CircuitBreakerClassifier]:
// a caller cancellation (see [IsCallerCancellation]) is [OutcomeNeutral], and
// every other call is classified as by default. The limits listed on
// IsCallerCancellation apply: a hanging downstream opens the breaker only when
// an r8e deadline ends the call before the caller gives up.
func CallerCancellationIsNeutral(ctx context.Context, err error) Outcome {
	if IsCallerCancellation(ctx, err) {
		return OutcomeNeutral
	}

	return classifyByError(ctx, err)
}

// IsLocalRejection reports whether err is one of [ErrRateLimited],
// [ErrBulkheadFull], [ErrBulkheadTimeout], [ErrCoDelShed] or
// [ErrConcurrencyLimited] itself, not an error that wraps it. That is exactly
// what a rate limiter, a bulkhead or an adaptive concurrency limiter running
// inside the circuit breaker returns when it turns a call away, before the
// call reaches the downstream.
//
// In a [Policy], those guards run inside the breaker, and by default the
// breaker counts their rejections as failures: a burst past the bulkhead's
// size can open the breaker on a healthy downstream. Make them neutral in the
// classifier:
//
//	r8e.CircuitBreakerClassifier(func(ctx context.Context, err error) r8e.Outcome {
//		if r8e.IsLocalRejection(err) {
//			return r8e.OutcomeNeutral
//		}
//		return r8e.CallerCancellationIsNeutral(ctx, err)
//	})
//
// A wrapped sentinel does not match. A nested policy inside the function
// returns its guards' rejections bare: its own retry runs inside its rate
// limiter, bulkhead and adaptive limiter, so it never sees them. What the
// outer breaker sees then depends on a retry that encloses the call into the
// nested policy, such as the outer policy's own [WithRetry], which runs inside
// its breaker. When that retry runs out of attempts, it wraps the last error
// ("retries exhausted: bulkhead full"), which is no local rejection, even if
// every attempt was turned away locally. When it stops on a RetryIf returning
// false or on an exhausted retry budget, it returns the bare sentinel, a local
// rejection, even after attempts that reached the downstream. The same holds
// for a function that returns one of these sentinels itself. To make such
// calls count, have the function return an error of its own, wrapping or
// replacing the sentinel. And when calls that
// ignore their context keep holding the bulkhead's slots, its rejections are
// the only sign left that the downstream hangs; keep [WithTimeout] and a
// function that honours its context.
//
// It reports false for [ErrThrottled] and [ErrSLOShed], whose shedders run
// outside the breaker and react to the downstream's recent outcomes, and for
// [ErrCircuitOpen], which an inner policy's breaker returns when its
// downstream is down.
func IsLocalRejection(err error) bool {
	// A wrapped sentinel is not a local rejection: identity is the contract.
	switch err {
	case ErrRateLimited, ErrBulkheadFull, ErrBulkheadTimeout, //nolint:errorlint // identity, see above
		ErrCoDelShed, ErrConcurrencyLimited:
		return true
	default:
		return false
	}
}

// String returns "success", "failure" or "neutral", and "unknown" for any
// other value.
func (o Outcome) String() string {
	switch o {
	case OutcomeSuccess:
		return "success"
	case OutcomeFailure:
		return "failure"
	case OutcomeNeutral:
		return "neutral"
	default:
		return "unknown"
	}
}

// Done reports the call's outcome and latency to the breaker that granted the
// permit. Only the first call on this CircuitPermit variable has an effect
// (see [CircuitPermit] about copies); later calls, and calls on the zero
// CircuitPermit returned with an Acquire error, do nothing. An outcome whose
// admission has gone stale is ignored (see [CircuitPermit]) and only fires
// OnCircuitStaleOutcome; otherwise a neutral outcome fires OnCircuitNeutral
// (see [Hooks]).
//
// elapsed is the call's latency, measured with the breaker's [Clock]; it
// feeds slow-call detection (see [SlowCallRate]).
func (p *CircuitPermit) Done(elapsed time.Duration, outcome Outcome) {
	if p.cb == nil || p.reported {
		return
	}

	p.reported = true
	p.cb.complete(p, elapsed, outcome)
}

// classifyByError is the default classifier: a nil error is a success and any
// other error a failure.
func classifyByError(_ context.Context, err error) Outcome {
	if err != nil {
		return OutcomeFailure
	}

	return OutcomeSuccess
}

// policyDeadlineEnded reports whether ctx ended on a deadline r8e set: ctx
// ended on a deadline, and its deadline is the one recorded under
// policyDeadlineKey by the nearest binding r8e deadline (see withPolicyTimeout
// and budgetDeadlineCtx.Value). Both instants are fixed when the contexts are
// created, so the answer does not depend on when it is asked. Any other
// deadline reads as the caller's: one set below an r8e deadline is earlier than
// the recorded instant, and one set under [context.WithoutCancel], which keeps
// the recorded value but drops the deadline it describes, can be later.
func policyDeadlineEnded(ctx context.Context) bool {
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return false
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		return false
	}

	policyDeadline, ok := ctx.Value(policyDeadlineKey{}).(time.Time)

	return ok && deadline.Equal(policyDeadline)
}
