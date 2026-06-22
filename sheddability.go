package r8e

import "context"

type (
	// Sheddability controls how the adaptive throttler ([WithAdaptiveThrottle])
	// treats a specific call under load. Stamp a context with [WithSheddability]
	// before calling [Policy.Do]; the throttler reads the stamp when deciding
	// whether to admit or shed the call.
	//
	// Scope: only the adaptive throttler consults this stamp. The circuit breaker,
	// rate limiter, and bulkhead are not affected — a [SheddabilityNever] call can
	// still be rejected by those patterns if they are saturated.
	//
	// Window participation: every call — regardless of its [Sheddability] — is
	// counted as a request in the sliding window, and backend errors from admitted
	// calls are counted as rejections. This keeps the overload signal accurate. As a
	// consequence, a burst of [SheddabilityNever] calls that fail at the backend
	// raises the rejection probability for subsequent [SheddabilityDefault] calls.
	//
	// Without any stamp the throttler applies its normal SRE probability formula;
	// [SheddabilityDefault] is the zero value and therefore requires no annotation.
	//
	// Only the three named constants are supported. Values outside
	// {-1, 0, 1} fall silently to [SheddabilityDefault] behaviour.
	Sheddability int8

	sheddabilityKey struct{}
)

const (
	// SheddabilityDefault is the zero value: the call is shed with the normal SRE
	// probability formula (proportional to backend load).
	SheddabilityDefault Sheddability = 0

	// SheddabilityNever bypasses the adaptive throttler: the call is always admitted
	// regardless of the current rejection probability. Use sparingly — only for
	// traffic that must reach the backend even under maximum load (e.g. liveness
	// probes sent via a standalone [Throttler] without a circuit breaker). Note that
	// [SheddabilityNever] does NOT bypass the circuit breaker, rate limiter, or
	// bulkhead; combine it with policy-level isolation when full bypass is needed.
	SheddabilityNever Sheddability = 1

	// SheddabilityAlways marks the call as sheddable: the throttler drops it as
	// soon as any load shedding is active (probability > 0), so it is the first
	// kind of traffic sacrificed under overload. Use for background jobs,
	// speculative pre-fetches, or non-user-facing work. The call is still admitted
	// when the backend is healthy (probability == 0).
	SheddabilityAlways Sheddability = -1
)

// WithSheddability stamps ctx with the given [Sheddability], returning the
// derived context. The adaptive throttler reads the stamp on each [Policy.Do]
// call; other patterns (rate limiter, bulkhead, …) are unaffected.
//
// The stamp is propagated through child contexts (e.g. [context.WithCancel],
// [context.WithTimeout]). It is NOT propagated through [WithCoalesce]: request
// coalescing runs the shared call under a detached context so that no single
// caller's cancellation can abort the group — a side-effect is that the
// sheddability of the first coalesced caller is not visible to the throttler.
func WithSheddability(ctx context.Context, s Sheddability) context.Context {
	return context.WithValue(ctx, sheddabilityKey{}, s)
}

// SheddabilityFromCtx returns the [Sheddability] stamped on ctx by
// [WithSheddability], or [SheddabilityDefault] if none was set. Exported so
// that wrappers built on top of r8e (e.g. middleware layers) can read the stamp
// for routing or logging without re-stamping.
func SheddabilityFromCtx(ctx context.Context) Sheddability {
	s, ok := ctx.Value(sheddabilityKey{}).(Sheddability)
	if !ok {
		return SheddabilityDefault
	}

	return s
}
