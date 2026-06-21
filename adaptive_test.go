package r8e

import (
	"context"
	"encoding/binary"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Construction and clamping
// ---------------------------------------------------------------------------

func TestAdaptiveDefaults(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{})

	assert.Equal(t, defaultInitialLimit, a.Limit())
	assert.Equal(t, 0, a.InFlight())
	assert.False(t, a.Saturated())
}

func TestAdaptiveClampsInvalidParams(t *testing.T) {
	t.Parallel()

	// minLimit below 1, maxLimit below minLimit, tolerance below 1, and an
	// initial limit above the band all fall back into a sane configuration.
	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{},
		MinLimit(0), MaxLimit(-5), RTTTolerance(0.2), InitialLimit(1000))

	// minLimit clamped to 1, maxLimit clamped up to minLimit (1), so the band is
	// [1,1] and the initial limit is pinned to 1.
	assert.Equal(t, 1, a.Limit())
	assert.InDelta(t, defaultRTTTolerance, a.tolerance, 1e-9)
}

func TestAdaptiveClampsInitialIntoBand(t *testing.T) {
	t.Parallel()

	// An initial limit below the floor is raised to it.
	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{},
		MinLimit(10), MaxLimit(50), InitialLimit(2))
	assert.Equal(t, 10, a.Limit())

	// An initial limit above the ceiling is lowered to it.
	b := NewAdaptiveLimiter(&stubClock{}, &Hooks{},
		MinLimit(10), MaxLimit(50), InitialLimit(99))
	assert.Equal(t, 50, b.Limit())
}

// ---------------------------------------------------------------------------
// Acquire / Record / accessors
// ---------------------------------------------------------------------------

func TestAdaptiveAcquireAdmitsUnderLimit(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{}, InitialLimit(3))

	for range 3 {
		_, err := a.Acquire()
		require.NoError(t, err)
	}

	assert.Equal(t, 3, a.InFlight())
	assert.True(t, a.Saturated())
}

func TestAdaptiveAcquireRejectsAtLimit(t *testing.T) {
	t.Parallel()

	var rejected int

	a := NewAdaptiveLimiter(&stubClock{},
		&Hooks{OnConcurrencyRejected: func() { rejected++ }},
		InitialLimit(2), MinLimit(1))

	_, err1 := a.Acquire()
	require.NoError(t, err1)
	_, err2 := a.Acquire()
	require.NoError(t, err2)

	// Third call is over the limit.
	_, err3 := a.Acquire()
	require.ErrorIs(t, err3, ErrConcurrencyLimited)
	assert.Equal(t, 1, rejected)
}

func TestAdaptiveRecordDecrementsInFlight(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	a := NewAdaptiveLimiter(clk, &Hooks{}, InitialLimit(4))

	done, err := a.Acquire()
	require.NoError(t, err)
	assert.Equal(t, 1, a.InFlight())

	clk.setElapsed(time.Millisecond)
	done()
	assert.Equal(t, 0, a.InFlight())
}

// ---------------------------------------------------------------------------
// Gradient2 controller math (white-box, via recompute)
// ---------------------------------------------------------------------------

func TestAdaptiveAppLimitedDoesNotGrow(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{}, InitialLimit(20))

	// In-flight well below half the limit: the controller stays put.
	for range 50 {
		a.recompute(time.Millisecond, 1)
	}

	assert.Equal(t, 20, a.Limit())
}

func TestAdaptiveGrowsUnderSteadyLoad(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{}, InitialLimit(20), MaxLimit(200))

	// Loaded with steady latency: no queueing detected, so the limit climbs.
	for range 100 {
		a.recompute(10*time.Millisecond, 100)
	}

	assert.Greater(t, a.Limit(), 20)
}

// TestAdaptiveGrowthStepMagnitude pins the exact growth step so a regression in
// the smoothing or queue-size constants (which direction-only assertions miss)
// is caught. Under steady RTT the gradient is pinned at 1.0, so each loaded step
// adds queueSize*smoothing = 4*0.2 = 0.8; ten steps grow 20 -> 28.
func TestAdaptiveGrowthStepMagnitude(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{}, InitialLimit(20), MaxLimit(200))

	for range 10 {
		a.recompute(10*time.Millisecond, 100)
	}

	assert.InDelta(t, 28.0, a.limit, 0.01)
}

func TestAdaptiveShrinksUnderRisingRTT(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{},
		InitialLimit(50), MinLimit(1), MaxLimit(200))

	// Establish a low baseline under load (also crosses the long-window warmup).
	for range 700 {
		a.recompute(10*time.Millisecond, 100)
	}

	grown := a.Limit()

	// Latency spikes 10x: queueing detected, the limit is pulled down.
	for range 50 {
		a.recompute(100*time.Millisecond, 100)
	}

	assert.Less(t, a.Limit(), grown)
}

// TestAdaptiveBaselineDecayCorrection isolates the longRTT/sample > 2 decay
// branch: with a high baseline already established (post-warmup), one sharply
// faster sample folds in (a tiny EMA step) and THEN the *0.95 decay applies. The
// assertion pins the decayed magnitude, so deleting the decay branch (which
// would leave longRTT at the folded value) fails the test.
func TestAdaptiveBaselineDecayCorrection(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{}, InitialLimit(20))

	const old = float64(100 * time.Millisecond)

	a.samples = adaptiveLongWindow
	a.longRTT = old

	sample := float64(10 * time.Millisecond)
	a.foldBaseline(sample)

	// foldBaseline first folds (window = adaptiveLongWindow post-warmup), then
	// decays by 0.95 because old/sample = 10 > 2.
	folded := old + (sample-old)/adaptiveLongWindow
	assert.InDelta(t, folded*0.95, a.longRTT, 1.0)
}

func TestAdaptiveClampsToMaxLimit(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{}, InitialLimit(20), MaxLimit(30))

	for range 300 {
		a.recompute(10*time.Millisecond, 100)
	}

	assert.Equal(t, 30, a.Limit())
}

func TestAdaptiveClampsToMinLimit(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{}, InitialLimit(20), MinLimit(15))

	// Position just at the floor with a low baseline, then a huge RTT drives the
	// raw estimate below the floor; it must clamp at minLimit, not under it.
	a.limit = 15
	a.longRTT = float64(time.Millisecond)
	a.samples = adaptiveLongWindow

	a.recompute(time.Second, 30)

	assert.Equal(t, 15, a.Limit())
}

func TestAdaptiveUpdateZeroRTTGuard(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{}, InitialLimit(20))

	// A zero-duration sample must not divide by zero or produce NaN.
	a.recompute(0, 20)

	assert.GreaterOrEqual(t, a.Limit(), 1)
}

// ---------------------------------------------------------------------------
// OnConcurrencyLimitChanged hook
// ---------------------------------------------------------------------------

func TestAdaptiveRecordEmitsLimitChanged(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var changed []int

	a := NewAdaptiveLimiter(clk,
		&Hooks{OnConcurrencyLimitChanged: func(l int) { changed = append(changed, l) }},
		InitialLimit(20), MaxLimit(200))

	// Position just below an integer boundary with an established baseline, then
	// a steady (non-queueing) sample nudges the integer limit up by one.
	a.limit = 20.9
	a.longRTT = float64(5 * time.Millisecond)
	a.samples = adaptiveLongWindow
	a.inFlight = 21

	clk.setElapsed(5 * time.Millisecond)
	a.complete(time.Time{})

	require.Len(t, changed, 1)
	assert.Equal(t, 21, changed[0])
}

func TestAdaptiveCompleteEmitsLimitDecrease(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var changed []int

	a := NewAdaptiveLimiter(clk,
		&Hooks{OnConcurrencyLimitChanged: func(l int) { changed = append(changed, l) }},
		InitialLimit(50), MinLimit(1), MaxLimit(200))

	// Low baseline, loaded, then a 20x-slower sample pulls the limit down across
	// an integer boundary — the hook must fire on the decrease, not just growth.
	a.limit = 50
	a.longRTT = float64(5 * time.Millisecond)
	a.samples = adaptiveLongWindow
	a.inFlight = 50

	clk.setElapsed(100 * time.Millisecond)
	a.complete(time.Time{})

	require.Len(t, changed, 1)
	assert.Less(t, changed[0], 50)
}

func TestAdaptiveRecordNoHookWhenLimitUnchanged(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var changed int

	a := NewAdaptiveLimiter(clk,
		&Hooks{OnConcurrencyLimitChanged: func(int) { changed++ }},
		InitialLimit(20))

	// App-limited record: the limit does not move, so no event fires.
	done, err := a.Acquire()
	require.NoError(t, err)

	clk.setElapsed(time.Millisecond)
	done()

	assert.Zero(t, changed)
}

// ---------------------------------------------------------------------------
// Reconfigure
// ---------------------------------------------------------------------------

func TestAdaptiveReconfigureNarrowsBandAndClampsLimit(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{}, InitialLimit(50), MaxLimit(200))

	// Lowering the ceiling below the live limit clamps it down immediately.
	a.Reconfigure(MaxLimit(30))
	assert.Equal(t, 30, a.Limit())

	// Raising the floor above the live limit clamps it up.
	a.Reconfigure(MinLimit(40))
	assert.Equal(t, 40, a.Limit())
}

func TestAdaptiveReconfigurePreservesUnspecified(t *testing.T) {
	t.Parallel()

	a := NewAdaptiveLimiter(&stubClock{}, &Hooks{},
		InitialLimit(20), MaxLimit(200), RTTTolerance(2.0))

	// Reconfiguring only the tolerance leaves the band untouched.
	a.Reconfigure(RTTTolerance(3.0))

	assert.InDelta(t, 3.0, a.tolerance, 1e-9)
	assert.InDelta(t, 200.0, a.maxLimit, 1e-9)
}

// ---------------------------------------------------------------------------
// Concurrency: -race safety
// ---------------------------------------------------------------------------

func TestAdaptiveConcurrentAcquireRecordRaceSafe(t *testing.T) {
	t.Parallel()

	// Real clock: this test only asserts the absence of data races and a sane
	// final state under concurrent Acquire/Record/Reconfigure.
	a := NewAdaptiveLimiter(RealClock{}, &Hooks{}, InitialLimit(50), MaxLimit(100))

	var wg sync.WaitGroup

	const workers = 50

	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()

			for range 100 {
				done, err := a.Acquire()
				if err != nil {
					continue
				}

				done()
			}
		}()
	}

	wg.Add(1)

	go func() {
		defer wg.Done()

		for range 100 {
			a.Reconfigure(MaxLimit(80))
		}
	}()

	wg.Wait()

	assert.Equal(t, 0, a.InFlight())
	assert.GreaterOrEqual(t, a.Limit(), 1)
}

// ---------------------------------------------------------------------------
// Policy integration
// ---------------------------------------------------------------------------

func TestWithAdaptiveConcurrencyAndBulkheadPanics(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t, ErrConcurrencyLimiterConflict, func() {
		_ = NewPolicy[string]("p",
			WithBulkhead(5),
			WithAdaptiveConcurrency(),
		)
	})
}

func TestWithAdaptiveConcurrencyObservability(t *testing.T) {
	t.Parallel()

	var rejected atomic.Int64

	policy := NewPolicy[string]("adaptive-svc",
		WithHooks(&Hooks{OnConcurrencyRejected: func() { rejected.Add(1) }}),
		// Pin the limit at 1 so a second concurrent call is rejected.
		WithAdaptiveConcurrency(InitialLimit(1), MinLimit(1), MaxLimit(1)),
	)

	release := make(chan struct{})
	started := make(chan struct{})

	go func() {
		_, _ = policy.Do(context.Background(), func(context.Context) (string, error) {
			close(started)
			<-release

			return "ok", nil
		})
	}()
	<-started

	// The single slot is held: metrics and health reflect saturation.
	m := policy.Metrics()
	assert.Equal(t, int64(1), m.ConcurrencyLimit)
	assert.Equal(t, int64(1), m.ConcurrencyInFlight)

	status := policy.HealthStatus()
	assert.Contains(t, status.Conditions, ConditionConcurrencyLimited)
	assert.True(t, status.Healthy) // degraded, not critical

	// A concurrent call is rejected.
	_, err := policy.Do(context.Background(), func(context.Context) (string, error) {
		return "unexpected", nil
	})
	require.ErrorIs(t, err, ErrConcurrencyLimited)
	assert.Equal(t, int64(1), rejected.Load())
	assert.Equal(t, int64(1), policy.Metrics().ConcurrencyRejected)

	close(release)
}

// ---------------------------------------------------------------------------
// Config build and reconfigure
// ---------------------------------------------------------------------------

func TestBuildOptionsAdaptiveConcurrency(t *testing.T) {
	t.Parallel()

	initial := 30
	minLimit := 5
	maxLimit := 120
	tol := 2.0

	opts, err := BuildOptions(&PolicyConfig{
		AdaptiveConcurrency: &AdaptiveConfig{
			InitialLimit: &initial,
			MinLimit:     &minLimit,
			MaxLimit:     &maxLimit,
			RTTTolerance: &tol,
		},
	})
	require.NoError(t, err)

	policy := NewPolicy[string]("cfg", opts...)
	assert.Equal(t, int64(30), policy.Metrics().ConcurrencyLimit)
}

func TestBuildOptionsBulkheadAdaptiveConflict(t *testing.T) {
	t.Parallel()

	bulkhead := 10

	_, err := BuildOptions(&PolicyConfig{
		Bulkhead:            &bulkhead,
		AdaptiveConcurrency: &AdaptiveConfig{},
	})
	require.ErrorIs(t, err, ErrConcurrencyLimiterConflict)
	require.ErrorContains(t, err, "adaptive_concurrency")
}

func TestReconfigureAdaptiveConcurrency(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string]("recfg",
		WithAdaptiveConcurrency(InitialLimit(50), MaxLimit(200)))

	maxLimit := 40
	err := policy.Reconfigure(PolicyConfig{
		AdaptiveConcurrency: &AdaptiveConfig{MaxLimit: &maxLimit},
	})
	require.NoError(t, err)

	assert.Equal(t, int64(40), policy.Metrics().ConcurrencyLimit)
}

func TestReconfigureAdaptiveAbsent(t *testing.T) {
	t.Parallel()

	// A policy without an adaptive limiter cannot be reconfigured into one.
	policy := NewPolicy[string]("no-adaptive", WithBulkhead(5))

	maxLimit := 40
	err := policy.Reconfigure(PolicyConfig{
		AdaptiveConcurrency: &AdaptiveConfig{MaxLimit: &maxLimit},
	})
	require.ErrorIs(t, err, ErrPatternAbsent)
	require.ErrorContains(t, err, "adaptive_concurrency")
}

// FuzzAdaptiveRecompute drives the Gradient2 controller through an arbitrary
// sequence of (rtt, inflight) samples decoded from the fuzz input, asserting
// the limit stays finite and within [minLimit, maxLimit] and the latency
// baseline stays finite after every step — the invariants a float feedback
// loop is most likely to break on degenerate inputs (zero, huge, negative).
func FuzzAdaptiveRecompute(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 10, 0, 0, 0, 50})            // 10ns rtt, inflight 50
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})              // zero rtt, zero inflight
	f.Add([]byte{255, 255, 255, 255, 255, 255, 255, 255, 0, 0, 0, 1}) // -1ns rtt

	f.Fuzz(func(t *testing.T, data []byte) {
		limiter := NewAdaptiveLimiter(&stubClock{}, &Hooks{},
			InitialLimit(20), MinLimit(1), MaxLimit(200))

		// Each 12-byte chunk is one sample: 8 bytes rtt, 4 bytes inflight.
		for len(data) >= 12 {
			rtt := time.Duration(int64(binary.BigEndian.Uint64(data[:8])))
			inflight := int(int32(binary.BigEndian.Uint32(data[8:12])))
			data = data[12:]

			limiter.recompute(rtt, inflight)

			if math.IsNaN(limiter.limit) || math.IsInf(limiter.limit, 0) {
				t.Fatalf("limit became non-finite: %v (rtt=%v inflight=%d)", limiter.limit, rtt, inflight)
			}

			if limiter.limit < limiter.minLimit || limiter.limit > limiter.maxLimit {
				t.Fatalf("limit %v left band [%v, %v] (rtt=%v inflight=%d)",
					limiter.limit, limiter.minLimit, limiter.maxLimit, rtt, inflight)
			}

			if math.IsNaN(limiter.longRTT) || math.IsInf(limiter.longRTT, 0) {
				t.Fatalf("longRTT became non-finite: %v (rtt=%v)", limiter.longRTT, rtt)
			}
		}
	})
}
