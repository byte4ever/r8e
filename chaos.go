package r8e

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"
)

// ---------------------------------------------------------------------------
// Chaos injection — Polly-v8 / Simmy-style fault, latency, outcome, behavior
// ---------------------------------------------------------------------------.

// Pattern: Chaos Injection — deliberately injects faults, latency, fake
// outcomes, or side-effect behaviors into the chain so a policy's own
// resilience patterns can be exercised. It sits innermost (see priorityChaos),
// so every configured pattern wraps it: an injected fault is retried by
// [WithRetry], injected latency is bounded by [WithTimeout], and a panic from a
// chaos behavior is caught by [WithRecover].

type (
	// chaosKind identifies which of the four injection strategies a
	// [ChaosStrategy] represents. It drives the per-call dispatch in chaos.Do and
	// labels the OnChaosInjected hook.
	chaosKind uint8

	// chaosCommon holds the per-strategy fields shared by [ChaosStrategy] and its
	// typed counterpart preparedChaos. Only the outcome function differs between
	// the two (erased any vs. typed func), so embedding this struct in both keeps
	// the field set in one place — adding a future knob touches one struct, and
	// prepareChaos copies the common fields as a single structural assignment
	// rather than a hand-enumerated field list that could silently omit one.
	// Fields are ordered pointer-first for betteralign; see ChaosStrategy and
	// preparedChaos, which place their own outcome field before the embed so the
	// whole struct stays optimally packed.
	chaosCommon struct {
		// faultErr is the error a kindFault strategy injects; defaulted to
		// [ErrChaosInjected] when the caller passes nil.
		faultErr error
		// enabled gates the strategy per call (see [ChaosEnabled]); nil means
		// always eligible. It is evaluated before the probability draw so a
		// canary predicate can switch chaos off in production without a redeploy.
		enabled func(context.Context) bool
		// behavior is a kindBehavior strategy's side-effect run before the call.
		behavior func(context.Context)
		// delay is the latency a kindLatency strategy injects on the policy clock.
		delay time.Duration
		// prob is the injection probability in [0, 1]; clamped on construction.
		prob float64
		kind chaosKind
	}

	// ChaosStrategy is one prepared chaos injection — a fault, a latency, a fake
	// outcome, or a side-effect behavior — produced by [ChaosFault],
	// [ChaosLatency], [ChaosOutcome], or [ChaosBehavior] and passed to [WithChaos].
	// Its fields are unexported: a strategy is an opaque value constructed only
	// through those functions, like [CacheEntry].
	ChaosStrategy struct {
		// outcome holds a kindOutcome strategy's fake-result function, erased to
		// any (a func(context.Context) (T, error)) and asserted back to the
		// policy's T in prepareChaos, mirroring [WithFallback]'s type erasure.
		// Placed before the embed to keep the struct optimally aligned.
		outcome any
		chaosCommon
	}

	// ChaosStrategyOption configures a single [ChaosStrategy].
	//
	// Pattern: Functional Options — composable optional settings (currently only
	// [ChaosEnabled]) applied to a strategy, keeping the strategy constructors'
	// signatures stable as more per-strategy knobs are added.
	ChaosStrategyOption func(*ChaosStrategy)

	// preparedChaos is a [ChaosStrategy] whose erased outcome has been asserted
	// back to the policy's concrete T, ready for the typed dispatch in chaos.Do.
	preparedChaos[T any] struct {
		outcome func(context.Context) (T, error)
		chaosCommon
	}

	// chaos is the innermost middleware that runs the configured strategies in
	// order before delegating to the real call. It holds the policy clock (for
	// deterministic injected latency) and the instrumented hooks (for
	// OnChaosInjected and the ChaosInjected counter).
	chaos[T any] struct {
		clock Clock
		hooks *Hooks
		// sampler draws the [0, 1) value compared against each strategy's
		// probability. It is rand.Float64 in production and is overridden only by
		// white-box tests, which set it before any concurrent Do.
		sampler    func() float64
		strategies []preparedChaos[T]
	}

	// chaosDesc holds the deferred chaos configuration. A non-nil pointer with at
	// least one strategy marks chaos injection as requested; newChaos asserts each
	// outcome strategy back to the policy's T in NewPolicy[T].
	chaosDesc struct {
		strategies []ChaosStrategy
	}
)

const (
	kindFault chaosKind = iota
	kindLatency
	kindOutcome
	kindBehavior
)

// String returns the lowercase label passed to the OnChaosInjected hook.
func (k chaosKind) String() string {
	switch k {
	case kindFault:
		return "fault"
	case kindLatency:
		return "latency"
	case kindOutcome:
		return "outcome"
	case kindBehavior:
		return "behavior"
	default:
		return "unknown"
	}
}

// ChaosEnabled gates a chaos strategy per call: the strategy is eligible to
// inject only when pred returns true for the call's context. Use it for safe,
// canary-style chaos in production — read a feature flag or a request header from
// ctx and return whether this call should be subject to chaos — so injection can
// be switched off at runtime without removing the strategy. A nil predicate (the
// default) leaves the strategy always eligible.
func ChaosEnabled(pred func(context.Context) bool) ChaosStrategyOption {
	return func(s *ChaosStrategy) {
		s.enabled = pred
	}
}

// ChaosFault returns a strategy that injects err (defaulting to
// [ErrChaosInjected] when nil) on a fraction prob of calls, short-circuiting the
// real call. Use it to verify that a [WithRetry], [WithCircuitBreaker], or
// [WithFallback] reacts to the injected failure. prob is clamped to [0, 1].
func ChaosFault(prob float64, err error, opts ...ChaosStrategyOption) ChaosStrategy {
	faultErr := err
	if faultErr == nil {
		faultErr = ErrChaosInjected
	}

	return newStrategy(kindFault, prob, opts, func(s *ChaosStrategy) {
		s.faultErr = faultErr
	})
}

// ChaosLatency returns a strategy that delays a fraction prob of calls by delay
// before the real call runs, then proceeds to it. The delay is measured on the
// policy [Clock], so it is deterministic under a fake clock and is cancelled by a
// context deadline. Use it to verify that a [WithTimeout] or [WithHedge] reacts
// to the injected slowness. prob is clamped to [0, 1]; a non-positive delay is a
// no-op wait.
func ChaosLatency(prob float64, delay time.Duration, opts ...ChaosStrategyOption) ChaosStrategy {
	return newStrategy(kindLatency, prob, opts, func(s *ChaosStrategy) {
		s.delay = delay
	})
}

// ChaosOutcome returns a strategy that, on a fraction prob of calls,
// short-circuits the real call and returns whatever fn produces — a fabricated
// success value or an error. It is the typed counterpart of [ChaosFault]: use it
// to inject a plausible-but-wrong result and check that downstream validation
// catches it. fn's result type must match the policy's type parameter T; a
// mismatch panics in [NewPolicy], like [WithFallback]. prob is clamped to [0, 1];
// a nil fn makes the strategy inert.
func ChaosOutcome[T any](
	prob float64,
	fn func(context.Context) (T, error),
	opts ...ChaosStrategyOption,
) ChaosStrategy {
	return newStrategy(kindOutcome, prob, opts, func(s *ChaosStrategy) {
		s.outcome = fn
	})
}

// ChaosBehavior returns a strategy that runs fn (a side effect — log, sleep,
// corrupt shared state, spike a counter) before the real call on a fraction prob
// of calls, then proceeds to the call. Unlike the other strategies it never
// short-circuits. Use it to simulate environmental disturbance around a call.
// prob is clamped to [0, 1]; a nil fn makes the strategy inert.
func ChaosBehavior(prob float64, fn func(context.Context), opts ...ChaosStrategyOption) ChaosStrategy {
	return newStrategy(kindBehavior, prob, opts, func(s *ChaosStrategy) {
		s.behavior = fn
	})
}

// newStrategy builds a [ChaosStrategy] of the given kind: it clamps prob, sets
// the kind-specific field via set, then applies the caller's options. Centralising
// construction keeps prob clamping and option application identical across the
// four strategy constructors.
func newStrategy(
	kind chaosKind,
	prob float64,
	opts []ChaosStrategyOption,
	set func(*ChaosStrategy),
) ChaosStrategy {
	strategy := ChaosStrategy{
		chaosCommon: chaosCommon{kind: kind, prob: clampUnitInterval(prob)},
	}
	set(&strategy)

	for _, opt := range opts {
		opt(&strategy)
	}

	return strategy
}

// WithChaos adds chaos injection: a list of strategies — [ChaosFault],
// [ChaosLatency], [ChaosOutcome], [ChaosBehavior] — that probabilistically
// disturb the call so a policy's own resilience patterns can be exercised
// (does my retry catch the injected fault? does my timeout catch the injected
// latency?). Each strategy injects independently with its own probability; gate
// any of them per call with [ChaosEnabled] for safe canary chaos in production.
//
// Chaos sits innermost in the chain, so every other configured pattern wraps it:
// a retry re-rolls every strategy on each attempt, a timeout bounds injected
// latency, and a [WithRecover] catches a panic thrown by a chaos behavior.
// Strategies run in the order given; a fault or an outcome short-circuits the
// remaining strategies and the real call, so list a fault before a latency to
// skip the latency wait when the fault fires (matching Polly's recommended
// order).
//
// Because the outcome and behavior functions and the [ChaosEnabled] predicate are
// code, chaos is code-only — it is deliberately absent from [PolicyConfig],
// [BuildOptions], and [Policy.Reconfigure], like [WithCoalesce] and [WithCache].
// To switch chaos off at runtime, return false from a [ChaosEnabled] predicate.
//
// Each injection fires the OnChaosInjected hook (with the strategy kind) and
// increments the ChaosInjected metric. Calling it with no strategies adds
// nothing.
func WithChaos(strategies ...ChaosStrategy) Option {
	return optionFunc(func(s *policySetup) {
		s.chaos = &chaosDesc{strategies: strategies}
	})
}

// newChaos prepares the typed chaos runner: it asserts every outcome strategy's
// erased function back to the policy's concrete func(context.Context) (T, error),
// panicking on a mismatch (an outcome typed for a different T than the policy is a
// programmer error, mirroring the fallback and cache entries). The sampler
// defaults to rand.Float64; white-box tests override it before use.
func newChaos[T any](desc *chaosDesc, clock Clock, hooks *Hooks) *chaos[T] {
	prepared := make([]preparedChaos[T], 0, len(desc.strategies))
	for _, strategy := range desc.strategies {
		prepared = append(prepared, prepareChaos[T](strategy))
	}

	return &chaos[T]{
		clock:      clock,
		hooks:      hooks,
		strategies: prepared,
		sampler:    rand.Float64,
	}
}

// prepareChaos converts one constructed [ChaosStrategy] into its typed form: it
// copies the shared fields structurally (the embedded chaosCommon) and asserts an
// outcome strategy's erased function back to T via assertOutcome. A non-outcome
// strategy carries no outcome, so only the outcome kind is asserted.
func prepareChaos[T any](strategy ChaosStrategy) preparedChaos[T] {
	prepared := preparedChaos[T]{chaosCommon: strategy.chaosCommon}

	if strategy.kind == kindOutcome && strategy.outcome != nil {
		prepared.outcome = assertOutcome[T](strategy.outcome)
	}

	return prepared
}

// assertOutcome asserts a [ChaosOutcome] strategy's erased function back to the
// policy's concrete func(context.Context) (T, error), panicking with a clear
// message on a mismatch (an outcome typed for a different T than the policy is a
// programmer error, mirroring the fallback and cache entries).
func assertOutcome[T any](outcome any) func(context.Context) (T, error) {
	fn, ok := outcome.(func(context.Context) (T, error))
	if !ok {
		var zero T

		panic(fmt.Sprintf(
			"r8e: ChaosOutcome function has type %T, which does not match "+
				"policy result type func(context.Context) (%T, error)",
			outcome, zero,
		))
	}

	return fn
}

// Do runs each strategy in order. A strategy injects only when it is enabled for
// the context and wins its probability draw; on injection it fires
// OnChaosInjected and either short-circuits (fault, outcome), waits then
// continues (latency), or runs a side effect then continues (behavior). When no
// strategy short-circuits, the real call runs.
//
//nolint:ireturn // generic type parameter T, not an interface
func (c *chaos[T]) Do(
	ctx context.Context,
	next func(context.Context) (T, error),
) (T, error) {
	for i := range c.strategies {
		strategy := &c.strategies[i]
		if !c.injects(ctx, strategy) {
			continue
		}

		c.hooks.emitChaosInjected(strategy.kind.String())

		switch strategy.kind {
		case kindFault:
			var zero T

			return zero, strategy.faultErr
		case kindOutcome:
			return strategy.outcome(ctx)
		case kindLatency:
			if err := c.wait(ctx, strategy.delay); err != nil {
				var zero T

				return zero, err
			}
		case kindBehavior:
			strategy.behavior(ctx)
		default:
			// A kind with no case is a programmer error (a new kind added to the
			// enum without a dispatch here). Panic loudly rather than emit a phantom
			// OnChaosInjected for an injection that never happens.
			panic(fmt.Sprintf("r8e: unhandled chaos kind %d", strategy.kind))
		}
	}

	return next(ctx) //nolint:wrapcheck // caller's error returned as-is
}

// injects reports whether a strategy fires on this call: it must be inert-free
// (an outcome or behavior with a nil function never injects), enabled for the
// context, and win the probability draw. A zero probability never injects and a
// probability of 1 always does, both without consulting the sampler.
func (c *chaos[T]) injects(ctx context.Context, strategy *preparedChaos[T]) bool {
	if strategy.kind == kindOutcome && strategy.outcome == nil {
		return false
	}

	if strategy.kind == kindBehavior && strategy.behavior == nil {
		return false
	}

	if strategy.enabled != nil && !strategy.enabled(ctx) {
		return false
	}

	if strategy.prob <= 0 {
		return false
	}

	if strategy.prob >= 1 {
		return true
	}

	return c.sampler() < strategy.prob
}

// wait sleeps for d on the policy clock, returning early with the context error
// if the call is cancelled first. A non-positive delay returns immediately. The
// clock-driven timer keeps injected latency deterministic under a fake clock.
func (c *chaos[T]) wait(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	timer := c.clock.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C():
		return nil
	case <-ctx.Done():
		return ctx.Err() //nolint:wrapcheck // surfacing cancellation as-is
	}
}

func newChaosEntry[T any](desc *chaosDesc, clock Clock, hooks *Hooks) PatternEntry[T] {
	runner := newChaos[T](desc, clock, hooks)

	return PatternEntry[T]{
		Priority: priorityChaos,
		Name:     "chaos",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return runner.Do(ctx, next)
			}
		},
	}
}
