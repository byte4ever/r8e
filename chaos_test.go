package r8e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// constSampler returns a fixed draw, so a test controls exactly whether a
// strategy with a probability in (0, 1) injects.
func constSampler(v float64) func() float64 {
	return func() float64 { return v }
}

// firedClock hands out timers that have already fired: NewTimer returns a timer
// whose channel already holds a value, so chaos.wait takes its timer branch
// deterministically without a second goroutine.
type firedClock struct{}

func (firedClock) Now() time.Time                { return time.Unix(0, 0) }
func (firedClock) Since(time.Time) time.Duration { return 0 }

//nolint:ireturn // satisfies the Timer interface by design
func (firedClock) NewTimer(time.Duration) Timer {
	ch := make(chan time.Time, 1)
	ch <- time.Unix(0, 0)

	return &firedTimer{ch: ch}
}

type firedTimer struct{ ch chan time.Time }

func (t *firedTimer) C() <-chan time.Time    { return t.ch }
func (*firedTimer) Stop() bool               { return true }
func (*firedTimer) Reset(time.Duration) bool { return false }

// newTestChaos builds a chaos runner over the given strategies with a fixed
// sampler so injection is deterministic.
func newTestChaos[T any](
	sampler func() float64,
	clock Clock,
	hooks *Hooks,
	strategies ...ChaosStrategy,
) *chaos[T] {
	c := newChaos[T](&chaosDesc{strategies: strategies}, clock, hooks)
	c.sampler = sampler

	return c
}

// okNext is a next function that records its invocation and returns a value.
func okNext(called *bool) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		*called = true

		return "real", nil
	}
}

// ---------------------------------------------------------------------------
// chaosKind.String
// ---------------------------------------------------------------------------

func TestChaosKindString(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "fault", kindFault.String())
	assert.Equal(t, "latency", kindLatency.String())
	assert.Equal(t, "outcome", kindOutcome.String())
	assert.Equal(t, "behavior", kindBehavior.String())
	assert.Equal(t, "unknown", chaosKind(99).String())
}

// ---------------------------------------------------------------------------
// Construction & clamping
// ---------------------------------------------------------------------------

func TestChaosFaultDefaultsNilErrToSentinel(t *testing.T) {
	t.Parallel()

	s := ChaosFault(0.5, nil)

	assert.Equal(t, kindFault, s.kind)
	require.ErrorIs(t, s.faultErr, ErrChaosInjected)
}

func TestChaosFaultKeepsExplicitErr(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	s := ChaosFault(0.5, sentinel)

	require.ErrorIs(t, s.faultErr, sentinel)
}

func TestChaosStrategyClampsProbability(t *testing.T) {
	t.Parallel()

	assert.Zero(t, ChaosFault(-1, nil).prob, "negative prob clamps to 0")
	assert.InEpsilon(t, 1.0, ChaosLatency(2, time.Second).prob, 1e-9,
		"prob above 1 clamps to 1")
	assert.InEpsilon(t, 0.25, ChaosBehavior(0.25, func(context.Context) {}).prob, 1e-9)
}

func TestChaosEnabledSetsPredicate(t *testing.T) {
	t.Parallel()

	s := ChaosFault(1, nil, ChaosEnabled(func(context.Context) bool { return false }))

	require.NotNil(t, s.enabled)
	assert.False(t, s.enabled(context.Background()))
}

// ---------------------------------------------------------------------------
// injects — eligibility & probability
// ---------------------------------------------------------------------------

func TestChaosInjectsProbabilityBoundaries(t *testing.T) {
	t.Parallel()

	zero := prepareChaos[string](ChaosFault(0, nil))
	one := prepareChaos[string](ChaosFault(1, nil))

	// prob 0 short-circuits before the sampler: even a sampler that would beat any
	// positive threshold (a negative draw) must not inject (pins the `<= 0` guard,
	// not `< 0`).
	belowZero := newTestChaos[string](constSampler(-0.5), firedClock{}, &Hooks{})
	assert.False(t, belowZero.injects(context.Background(), &zero), "prob 0 never injects")

	// prob 1 short-circuits to true before the sampler: even a sampler at the top
	// of its range (1.0, where 1.0 < 1 is false) must still inject (pins the
	// `>= 1` guard, not `> 1`).
	atTop := newTestChaos[string](constSampler(1.0), firedClock{}, &Hooks{})
	assert.True(t, atTop.injects(context.Background(), &one), "prob 1 always injects")
}

func TestChaosInjectsConsultsSampler(t *testing.T) {
	t.Parallel()

	half := prepareChaos[string](ChaosFault(0.5, nil))

	below := newTestChaos[string](constSampler(0.4), firedClock{}, &Hooks{})
	assert.True(t, below.injects(context.Background(), &half), "sampler < prob injects")

	atOrAbove := newTestChaos[string](constSampler(0.5), firedClock{}, &Hooks{})
	assert.False(t, atOrAbove.injects(context.Background(), &half), "sampler >= prob does not inject")
}

func TestChaosInjectsRespectsEnabledPredicate(t *testing.T) {
	t.Parallel()

	c := newTestChaos[string](constSampler(0), firedClock{}, &Hooks{})

	disabled := prepareChaos[string](
		ChaosFault(1, nil, ChaosEnabled(func(context.Context) bool { return false })),
	)
	assert.False(t, c.injects(context.Background(), &disabled), "predicate false suppresses injection")

	enabled := prepareChaos[string](
		ChaosFault(1, nil, ChaosEnabled(func(context.Context) bool { return true })),
	)
	assert.True(t, c.injects(context.Background(), &enabled), "predicate true allows injection")
}

func TestChaosInjectsNilFunctionsAreInert(t *testing.T) {
	t.Parallel()

	c := newTestChaos[string](constSampler(0), firedClock{}, &Hooks{})

	outcome := prepareChaos[string](ChaosOutcome[string](1, nil))
	assert.False(t, c.injects(context.Background(), &outcome), "nil outcome never injects")

	behavior := prepareChaos[string](ChaosBehavior(1, nil))
	assert.False(t, c.injects(context.Background(), &behavior), "nil behavior never injects")
}

// ---------------------------------------------------------------------------
// Do — per-kind dispatch
// ---------------------------------------------------------------------------

func TestChaosDoFaultShortCircuits(t *testing.T) {
	t.Parallel()

	var kind string

	hooks := &Hooks{OnChaosInjected: func(k string) { kind = k }}
	c := newTestChaos[string](constSampler(0), firedClock{}, hooks,
		ChaosFault(1, nil))

	called := false
	_, err := c.Do(context.Background(), okNext(&called))

	require.ErrorIs(t, err, ErrChaosInjected)
	assert.False(t, called, "fault short-circuits the real call")
	assert.Equal(t, "fault", kind)
}

func TestChaosDoFaultReturnsCustomError(t *testing.T) {
	t.Parallel()

	custom := errors.New("custom downstream failure")
	c := newTestChaos[string](constSampler(0), firedClock{}, &Hooks{},
		ChaosFault(1, custom))

	_, err := c.Do(context.Background(), okNext(new(bool)))

	require.ErrorIs(t, err, custom, "Do returns the strategy's own error, not the sentinel")
	require.NotErrorIs(t, err, ErrChaosInjected)
}

func TestChaosDoOutcomeShortCircuits(t *testing.T) {
	t.Parallel()

	var kind string

	hooks := &Hooks{OnChaosInjected: func(k string) { kind = k }}
	c := newTestChaos[string](constSampler(0), firedClock{}, hooks,
		ChaosOutcome[string](1, func(context.Context) (string, error) {
			return "fabricated", nil
		}))

	called := false
	got, err := c.Do(context.Background(), okNext(&called))

	require.NoError(t, err)
	assert.Equal(t, "fabricated", got)
	assert.False(t, called, "outcome short-circuits the real call")
	assert.Equal(t, "outcome", kind, "the emitted hook kind matches the strategy")
}

func TestChaosDoOutcomeReturnsInjectedError(t *testing.T) {
	t.Parallel()

	injected := errors.New("fabricated failure outcome")
	c := newTestChaos[string](constSampler(0), firedClock{}, &Hooks{},
		ChaosOutcome[string](1, func(context.Context) (string, error) {
			return "", injected
		}))

	got, err := c.Do(context.Background(), okNext(new(bool)))

	require.ErrorIs(t, err, injected, "an erroring outcome propagates its error")
	assert.Empty(t, got)
}

func TestChaosDoBehaviorRunsThenContinues(t *testing.T) {
	t.Parallel()

	var kind string

	ran := false
	hooks := &Hooks{OnChaosInjected: func(k string) { kind = k }}
	c := newTestChaos[string](constSampler(0), firedClock{}, hooks,
		ChaosBehavior(1, func(context.Context) { ran = true }))

	called := false
	got, err := c.Do(context.Background(), okNext(&called))

	require.NoError(t, err)
	assert.True(t, ran, "behavior side effect ran")
	assert.True(t, called, "behavior does not short-circuit; the real call ran")
	assert.Equal(t, "real", got)
	assert.Equal(t, "behavior", kind, "the emitted hook kind matches the strategy")
}

func TestChaosDoLatencyWaitsThenContinues(t *testing.T) {
	t.Parallel()

	var kind string

	hooks := &Hooks{OnChaosInjected: func(k string) { kind = k }}
	c := newTestChaos[string](constSampler(0), firedClock{}, hooks,
		ChaosLatency(1, time.Second))

	called := false
	got, err := c.Do(context.Background(), okNext(&called))

	require.NoError(t, err)
	assert.True(t, called, "latency does not short-circuit; the real call ran after the wait")
	assert.Equal(t, "real", got)
	assert.Equal(t, "latency", kind, "the emitted hook kind matches the strategy")
}

func TestChaosDoUnhandledKindPanics(t *testing.T) {
	t.Parallel()

	// A prepared strategy with a kind outside the dispatch switch is a programmer
	// error (a new kind added without a case); Do must panic loudly rather than
	// emit a phantom injection and fall through.
	c := &chaos[string]{
		clock:   firedClock{},
		hooks:   &Hooks{},
		sampler: constSampler(0),
		strategies: []preparedChaos[string]{
			{chaosCommon: chaosCommon{kind: chaosKind(99), prob: 1}},
		},
	}

	require.PanicsWithValue(t, "r8e: unhandled chaos kind 99", func() {
		_, _ = c.Do(context.Background(), okNext(new(bool)))
	})
}

func TestChaosDoLatencyCancelledContextShortCircuits(t *testing.T) {
	t.Parallel()

	// stubClock hands out a never-firing timer, so the wait resolves on the
	// already-cancelled context.
	c := newTestChaos[string](constSampler(0), &stubClock{}, &Hooks{},
		ChaosLatency(1, time.Second))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	called := false
	_, err := c.Do(ctx, okNext(&called))

	require.ErrorIs(t, err, context.Canceled)
	assert.False(t, called, "a cancelled wait short-circuits before the real call")
}

func TestChaosDoNoInjectionCallsNext(t *testing.T) {
	t.Parallel()

	c := newTestChaos[string](constSampler(0.9), firedClock{}, &Hooks{},
		ChaosFault(0.5, nil)) // sampler 0.9 >= 0.5 → no injection

	called := false
	got, err := c.Do(context.Background(), okNext(&called))

	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, "real", got)
}

func TestChaosDoEvaluatesStrategiesInOrder(t *testing.T) {
	t.Parallel()

	// Fault listed before latency: the fault fires first and short-circuits, so
	// the latency wait is skipped entirely (Polly's recommended order).
	behaviorRan := false
	c := newTestChaos[string](constSampler(0), firedClock{}, &Hooks{},
		ChaosBehavior(1, func(context.Context) { behaviorRan = true }),
		ChaosFault(1, nil),
		ChaosLatency(1, time.Hour)) // would hang on a real wait if reached

	called := false
	_, err := c.Do(context.Background(), okNext(&called))

	require.ErrorIs(t, err, ErrChaosInjected)
	assert.True(t, behaviorRan, "behavior before the fault still ran")
	assert.False(t, called, "fault short-circuits the latency and the real call")
}

// ---------------------------------------------------------------------------
// wait
// ---------------------------------------------------------------------------

func TestChaosWaitNonPositiveReturnsImmediately(t *testing.T) {
	t.Parallel()

	c := newTestChaos[string](constSampler(0), &stubClock{}, &Hooks{})

	require.NoError(t, c.wait(context.Background(), 0))
	require.NoError(t, c.wait(context.Background(), -time.Second))
}

// ---------------------------------------------------------------------------
// prepareChaos — type assertion
// ---------------------------------------------------------------------------

func TestPrepareChaosOutcomeTypeMismatchPanics(t *testing.T) {
	t.Parallel()

	// An outcome typed for int built into a string runner is a programmer error.
	intOutcome := ChaosOutcome[int](1, func(context.Context) (int, error) { return 0, nil })

	require.PanicsWithValue(
		t,
		"r8e: ChaosOutcome function has type func(context.Context) (int, error), "+
			"which does not match policy result type func(context.Context) (string, error)",
		func() { prepareChaos[string](intOutcome) },
	)
}

// ---------------------------------------------------------------------------
// Entry builder
// ---------------------------------------------------------------------------

func TestNewChaosEntryMetadata(t *testing.T) {
	t.Parallel()

	entry := newChaosEntry[string](
		&chaosDesc{strategies: []ChaosStrategy{ChaosFault(1, nil)}},
		firedClock{}, &Hooks{},
	)

	assert.Equal(t, priorityChaos, entry.Priority)
	assert.Equal(t, "chaos", entry.Name)
}

// ---------------------------------------------------------------------------
// Integration through NewPolicy — deterministic prob 0/1
// ---------------------------------------------------------------------------

func TestWithChaosFaultRetriedThenFallback(t *testing.T) {
	t.Parallel()

	// Chaos always faults; retry re-rolls it every attempt (still always faults),
	// so retries are exhausted and the fallback serves.
	policy := NewPolicy[string](
		"",
		WithRetry(2, ConstantBackoff(time.Nanosecond)),
		WithFallback("fallback"),
		WithChaos(ChaosFault(1, nil)),
	)

	got, err := policy.Do(context.Background(),
		func(context.Context) (string, error) { return "real", nil })

	require.NoError(t, err)
	assert.Equal(t, "fallback", got)

	metrics := policy.Metrics()
	// More than one injection proves the fault is re-rolled on every retry attempt,
	// not just the first (assert.Positive would pass even with a single injection).
	assert.GreaterOrEqual(t, metrics.ChaosInjected, int64(2),
		"the fault is re-rolled on every attempt")
	assert.Positive(t, metrics.Retries, "the injected fault drove retries")
}

func TestWithChaosDisabledInjectsNothing(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string](
		"",
		WithChaos(ChaosFault(1, nil,
			ChaosEnabled(func(context.Context) bool { return false }))),
	)

	got, err := policy.Do(context.Background(),
		func(context.Context) (string, error) { return "real", nil })

	require.NoError(t, err)
	assert.Equal(t, "real", got, "a disabled strategy lets the real call through")
	assert.Zero(t, policy.Metrics().ChaosInjected)
}

func TestWithChaosNoStrategiesAddsNoMiddleware(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string]("", WithChaos())

	got, err := policy.Do(context.Background(),
		func(context.Context) (string, error) { return "real", nil })

	require.NoError(t, err)
	assert.Equal(t, "real", got)
}

func TestWithChaosLatencyCaughtByTimeout(t *testing.T) {
	t.Parallel()

	// Real clock: an injected 200ms latency far exceeds the 20ms timeout (a 10×
	// margin so a loaded runner does not flake), so the timeout fires and the
	// fallback covers it.
	policy := NewPolicy[string](
		"",
		WithTimeout(20*time.Millisecond),
		WithFallback("fallback"),
		WithChaos(ChaosLatency(1, 200*time.Millisecond)),
	)

	got, err := policy.Do(context.Background(),
		func(context.Context) (string, error) { return "real", nil })

	require.NoError(t, err)
	assert.Equal(t, "fallback", got)
	assert.Equal(t, int64(1), policy.Metrics().Timeouts, "the injected latency tripped the timeout")
}

func TestWithChaosOutcomeInjectsTypedValue(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[int](
		"",
		WithChaos(ChaosOutcome[int](1, func(context.Context) (int, error) {
			return 42, nil
		})),
	)

	got, err := policy.Do(context.Background(),
		func(context.Context) (int, error) { return 7, nil })

	require.NoError(t, err)
	assert.Equal(t, 42, got, "the fabricated outcome replaced the real result")
}

func TestWithChaosOutcomeTypeMismatchPanics(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() {
		NewPolicy[string](
			"",
			WithChaos(ChaosOutcome[int](1, func(context.Context) (int, error) {
				return 0, nil
			})),
		)
	})
}
