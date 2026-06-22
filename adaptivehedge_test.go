package r8e

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// adaptiveHedge — config resolution
// ---------------------------------------------------------------------------

// TestAdaptiveHedgeResolveDefaults proves every out-of-range or unset tunable
// falls back to its documented default rather than being rejected. Unlike the
// adaptive timeout, the hedge multiplier only has to be positive (a value at or
// below the percentile is a legitimate "hedge more aggressively" choice).
func TestAdaptiveHedgeResolveDefaults(t *testing.T) {
	t.Parallel()

	cfg := adaptiveHedgeConfig{} // all zero
	cfg.resolve()

	assert.InEpsilon(t, defaultAdaptiveHedgePercentile, cfg.percentile, 1e-9)
	assert.InEpsilon(t, defaultAdaptiveHedgeMultiplier, cfg.multiplier, 1e-9)
	assert.Equal(t, int64(defaultAdaptiveHedgeMinSamples), cfg.minSamples)
	assert.Equal(t, time.Duration(0), cfg.floor)

	// Out-of-range values reset; a negative floor disables the floor. A zero or
	// negative multiplier resets (positive is the only requirement).
	bad := adaptiveHedgeConfig{percentile: 1.5, multiplier: 0, minSamples: -3, floor: -time.Second}
	bad.resolve()

	assert.InEpsilon(t, defaultAdaptiveHedgePercentile, bad.percentile, 1e-9)
	assert.InEpsilon(t, defaultAdaptiveHedgeMultiplier, bad.multiplier, 1e-9)
	assert.Equal(t, int64(defaultAdaptiveHedgeMinSamples), bad.minSamples)
	assert.Equal(t, time.Duration(0), bad.floor)

	// In-range values are preserved, including a multiplier below 1 (more eager).
	good := adaptiveHedgeConfig{percentile: 0.99, multiplier: 0.5, minSamples: 50, floor: 5 * time.Millisecond}
	good.resolve()

	assert.InEpsilon(t, 0.99, good.percentile, 1e-9)
	assert.InEpsilon(t, 0.5, good.multiplier, 1e-9)
	assert.Equal(t, int64(50), good.minSamples)
	assert.Equal(t, 5*time.Millisecond, good.floor)
}

// ---------------------------------------------------------------------------
// adaptiveHedge — compute (warmup, percentile, clamping)
// ---------------------------------------------------------------------------

// newTestAdaptiveHedge builds an adaptiveHedge on a fixed-instant stub clock and
// records n successful samples of duration d into its window, all in one epoch.
func newTestAdaptiveHedge(cfg *adaptiveHedgeConfig, d time.Duration, n int) *adaptiveHedge {
	clk := &stubClock{now: epochBase()}
	ah := newAdaptiveHedge(cfg, clk)

	for range n {
		ah.record(d, nil)
	}

	return ah
}

// TestAdaptiveHedgeCompute table-drives the warmup gate and the floor/ceiling
// clamp. Every case records samples of 10ms, so once warm the raw estimate is
// p95×multiplier ≈ 10ms (default p95×1); the cases then probe each clamp edge.
func TestAdaptiveHedgeCompute(t *testing.T) {
	t.Parallel()

	const sample = 10 * time.Millisecond

	tests := map[string]struct {
		cfg     adaptiveHedgeConfig
		samples int
		ceiling time.Duration
		want    time.Duration // exact when clamped; 0 ⇒ assert ~10ms within accuracy
	}{
		"warmup below minSamples uses ceiling": {
			cfg:     adaptiveHedgeConfig{minSamples: 20},
			samples: 5,
			ceiling: time.Second,
			want:    time.Second,
		},
		"warm estimate is p95 times multiplier": {
			samples: 50,
			ceiling: time.Second,
			want:    0, // ~10ms, asserted within the DDSketch accuracy bound
		},
		"raw estimate above ceiling clamps to ceiling": {
			samples: 50,
			ceiling: 5 * time.Millisecond,
			want:    5 * time.Millisecond,
		},
		"raw estimate below floor lifts to floor": {
			cfg:     adaptiveHedgeConfig{floor: 100 * time.Millisecond},
			samples: 50,
			ceiling: time.Second,
			want:    100 * time.Millisecond,
		},
		"ceiling wins even when floor exceeds it": {
			cfg:     adaptiveHedgeConfig{floor: time.Second},
			samples: 50,
			ceiling: 50 * time.Millisecond,
			want:    50 * time.Millisecond,
		},
		"aggressive multiplier hedges earlier": {
			cfg:     adaptiveHedgeConfig{multiplier: 0.5},
			samples: 50,
			ceiling: time.Second,
			want:    0, // p95×0.5 ≈ 5ms — handled below via its own assertion
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := tc.cfg
			ah := newTestAdaptiveHedge(&cfg, sample, tc.samples)

			got := ah.compute(tc.ceiling)
			if tc.want != 0 {
				assert.Equal(t, tc.want, got)

				return
			}

			// The two "0" cases assert against their expected raw estimate.
			wantApprox := 10 * time.Millisecond
			if cfg.multiplier == 0.5 {
				wantApprox = 5 * time.Millisecond
			}

			assert.Less(t, relErr(got, wantApprox), 0.03,
				"want ~%s (p95×mult), got %s", wantApprox, got)
		})
	}
}

// ---------------------------------------------------------------------------
// adaptiveHedge — record only feeds successes (censoring)
// ---------------------------------------------------------------------------

// TestAdaptiveHedgeRecordOnlySuccess proves a failed primary, or one cancelled
// because the hedge won the race, is not folded into the percentile window — so a
// hedge-shortened outcome can never pull down the very delay that produced it.
func TestAdaptiveHedgeRecordOnlySuccess(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	ah := newAdaptiveHedge(&adaptiveHedgeConfig{}, clk)

	for range 25 {
		ah.record(10*time.Millisecond, nil)
	}

	_, samples := ah.window.quantileSnapshot(0.95)
	require.Equal(t, int64(25), samples)

	// A failed primary and a cancelled primary (hedge won) must not change the
	// window — both carry a non-nil error.
	for range 25 {
		ah.record(time.Second, errors.New("boom")) //nolint:err113 // test sentinel
		ah.record(time.Second, context.Canceled)
	}

	value, after := ah.window.quantileSnapshot(0.95)
	assert.Equal(t, int64(25), after, "non-success primaries must not be recorded")
	assert.Less(t, relErr(value, 10*time.Millisecond), 0.03,
		"p95 stays at the success latency, got %s", value)
}

// ---------------------------------------------------------------------------
// adaptiveHedge — reconfigure
// ---------------------------------------------------------------------------

// TestAdaptiveHedgeReconfigurePreservesUnset proves a runtime overlay keeps the
// fields it does not set and resets a field given an out-of-range value.
func TestAdaptiveHedgeReconfigurePreservesUnset(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	ah := newAdaptiveHedge(&adaptiveHedgeConfig{
		percentile: 0.99,
		multiplier: 1.5,
		floor:      50 * time.Millisecond,
		minSamples: 10,
	}, clk)

	ah.reconfigure(AdaptiveHedgeMultiplier(2))

	cfg := ah.cfg.Load()
	assert.InEpsilon(t, 0.99, cfg.percentile, 1e-9, "percentile preserved")
	assert.InEpsilon(t, 2.0, cfg.multiplier, 1e-9, "multiplier updated")
	assert.Equal(t, 50*time.Millisecond, cfg.floor, "floor preserved")
	assert.Equal(t, int64(10), cfg.minSamples, "minSamples preserved")

	// An explicit out-of-range value resets that field to its default.
	ah.reconfigure(AdaptiveHedgePercentile(0))

	cfg = ah.cfg.Load()
	assert.InEpsilon(t, defaultAdaptiveHedgePercentile, cfg.percentile, 1e-9)
	assert.InEpsilon(t, 2.0, cfg.multiplier, 1e-9, "other fields still preserved")
}

// TestAdaptiveHedgeReconfigurePercentileTakesEffectImmediately proves a percentile
// reconfigure is not masked by the per-epoch estimate cache: it is invalidated on
// reconfigure, so the new percentile drives compute on the very next call within
// the same window epoch (not only after the epoch rolls over).
func TestAdaptiveHedgeReconfigurePercentileTakesEffectImmediately(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	ah := newAdaptiveHedge(&adaptiveHedgeConfig{percentile: 0.50}, clk)

	// A skewed distribution: most calls fast (~5ms), a slow tail (~80ms) so p50 and
	// p99 are far apart and a percentile change is observable.
	for range 90 {
		ah.record(5*time.Millisecond, nil)
	}

	for range 10 {
		ah.record(80*time.Millisecond, nil)
	}

	low := ah.compute(time.Second) // p50×1 ≈ 5ms, caches this epoch

	ah.reconfigure(AdaptiveHedgePercentile(0.99))

	high := ah.compute(time.Second) // same epoch — must already reflect p99
	assert.Greater(t, high, 3*low,
		"p99 must dominate p50 immediately after reconfigure (got %s vs %s)", high, low)
}

// ---------------------------------------------------------------------------
// End-to-end censoring through the real ah.record wiring
// ---------------------------------------------------------------------------

// TestAdaptiveHedgeWindowFedByPrimaryNotWinningHedge closes the seam between
// DoHedge's RecordPrimary callback and the controller's record: it drives real
// hedged calls with RecordPrimary wired to the actual ah.record (exactly as
// newAdaptiveHedgeEntry wires it) and inspects the controller's own window. A
// winning primary must add a sample; a winning hedge — which cancels the primary —
// must NOT, or the percentile that sized the hedge delay would be biased by the
// hedge's own success (the feedback loop the feature forbids). A regression that
// dropped the primary's error between the callback and the window would survive the
// two unit halves but fail here.
func TestAdaptiveHedgeWindowFedByPrimaryNotWinningHedge(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ah := newAdaptiveHedge(&adaptiveHedgeConfig{}, RealClock{})

		// 1) Primary wins (completes before the hour-long hedge delay): fed through
		// the real ah.record, the window gains exactly one sample.
		_, err := DoHedge[string](
			context.Background(),
			func(_ context.Context) (string, error) { return "primary", nil },
			HedgeParams{Delay: time.Hour, Hooks: &Hooks{}, Clock: RealClock{}, RecordPrimary: ah.record},
		)
		require.NoError(t, err)
		synctest.Wait()

		_, samples := ah.window.quantileSnapshot(0.95)
		require.Equal(t, int64(1), samples, "a winning primary feeds the window")

		// 2) Hedge wins (primary is a straggler, cancelled): its censored primary
		// latency must NOT feed the window — the count stays at 1.
		var callCount atomic.Int32

		_, err = DoHedge[string](
			context.Background(),
			func(ctx context.Context) (string, error) {
				if callCount.Add(1) == 1 {
					select {
					case <-time.After(time.Hour):
						return "primary", nil
					case <-ctx.Done():
						return "", ctx.Err() //nolint:wrapcheck // surfacing cancellation
					}
				}

				return "hedge", nil
			},
			HedgeParams{Delay: 10 * time.Millisecond, Hooks: &Hooks{}, Clock: RealClock{}, RecordPrimary: ah.record},
		)
		require.NoError(t, err)
		synctest.Wait()

		_, samples = ah.window.quantileSnapshot(0.95)
		assert.Equal(t, int64(1), samples,
			"a winning hedge's cancelled primary must not feed the window")
	})
}

// ---------------------------------------------------------------------------
// Policy integration
// ---------------------------------------------------------------------------

// TestWithHedgeAdaptiveMetrics drives a policy whose primaries each measure 10ms
// and proves Metrics().AdaptiveHedgeDelay reports the adapted value (p95×1) once
// warm. The stub clock's timer never fires, so the primary always wins and feeds
// the window; the hedge itself never launches.
func TestWithHedgeAdaptiveMetrics(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase(), elapsed: 10 * time.Millisecond}
	policy := NewPolicy[string](
		"adaptive",
		WithClock(clk),
		WithRegistry(NewRegistry()),
		WithHedge(time.Second, AdaptiveHedge()),
	)

	fast := func(_ context.Context) (string, error) { return "ok", nil }

	for range 30 {
		_, err := policy.Do(context.Background(), fast)
		require.NoError(t, err)
	}

	// Advance one epoch so the per-epoch estimate refreshes against the 30 samples.
	clk.now = epochBase().Add(defaultLatencyWindow / latencyWindowBuckets)

	metrics := policy.Metrics()
	assert.Less(t, relErr(metrics.AdaptiveHedgeDelay, 10*time.Millisecond), 0.03,
		"want ~10ms (p95×1), got %s", metrics.AdaptiveHedgeDelay)
}

// TestWithHedgeFixedReportsNoAdaptiveHedge proves a plain WithHedge builds a
// non-adaptive hedge, leaving the AdaptiveHedgeDelay gauge at zero.
func TestWithHedgeFixedReportsNoAdaptiveHedge(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string](
		"fixed",
		WithRegistry(NewRegistry()),
		WithHedge(time.Second),
	)

	assert.Nil(t, policy.adaptiveHedge)
	assert.Zero(t, policy.Metrics().AdaptiveHedgeDelay)
}

// TestPolicyReconfigureAdaptiveHedge proves the adaptive tunables hot-reload via the
// adaptive_hedge config block, the ceiling reloads via the hedge field, and that
// targeting a non-adaptive hedge is rejected.
func TestPolicyReconfigureAdaptiveHedge(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	policy := NewPolicy[string](
		"adaptive",
		WithClock(clk),
		WithRegistry(NewRegistry()),
		WithHedge(time.Second, AdaptiveHedge()),
	)

	err := policy.Reconfigure(PolicyConfig{
		AdaptiveHedge: &AdaptiveHedgeConfig{Multiplier: f64Ptr(2)},
	})
	require.NoError(t, err)
	assert.InEpsilon(t, 2.0, policy.adaptiveHedge.cfg.Load().multiplier, 1e-9)

	// A bad floor string surfaces a parse error naming the offending field.
	err = policy.Reconfigure(PolicyConfig{
		AdaptiveHedge: &AdaptiveHedgeConfig{Floor: strPtr("nope")},
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "adaptive_hedge.floor")

	// A bad ceiling (hedge) duration string surfaces a parse error too.
	err = policy.Reconfigure(PolicyConfig{Hedge: strPtr("nope")})
	require.Error(t, err)
	assert.ErrorContains(t, err, "reconfigure hedge")

	// The ceiling itself reconfigures through the hedge field.
	require.NoError(t, policy.Reconfigure(PolicyConfig{Hedge: strPtr("2s")}))
	assert.Equal(t, int64(2*time.Second), policy.hedge.Load())

	// A non-adaptive hedge cannot have adaptation enabled at runtime.
	fixed := NewPolicy[string]("fixed", WithRegistry(NewRegistry()), WithHedge(time.Second))
	err = fixed.Reconfigure(PolicyConfig{
		AdaptiveHedge: &AdaptiveHedgeConfig{Multiplier: f64Ptr(3)},
	})
	require.ErrorIs(t, err, ErrAdaptiveHedgeWithoutHedge)
}

// TestBuildOptionsAdaptiveHedge proves the config path wires the adaptive tunables,
// rejects an adaptive block without a hedge, and reports a bad floor.
func TestBuildOptionsAdaptiveHedge(t *testing.T) {
	t.Parallel()

	// adaptive_hedge without hedge is rejected the same cold as hot.
	_, err := BuildOptions(&PolicyConfig{
		AdaptiveHedge: &AdaptiveHedgeConfig{Multiplier: f64Ptr(2)},
	})
	require.ErrorIs(t, err, ErrAdaptiveHedgeWithoutHedge)

	// A bad floor string fails to parse, naming the offending field.
	_, err = BuildOptions(&PolicyConfig{
		Hedge:         strPtr("1s"),
		AdaptiveHedge: &AdaptiveHedgeConfig{Floor: strPtr("nope")},
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "adaptive_hedge.floor")

	// A valid block builds a working adaptive policy.
	opts, err := BuildOptions(&PolicyConfig{
		Hedge: strPtr("1s"),
		AdaptiveHedge: &AdaptiveHedgeConfig{
			Percentile: f64Ptr(0.99),
			Multiplier: f64Ptr(1.5),
			Floor:      strPtr("5ms"),
			MinSamples: intPtr(50),
		},
	})
	require.NoError(t, err)

	policy := NewPolicy[string]("cfg", append(opts, WithRegistry(NewRegistry()))...)
	require.NotNil(t, policy.adaptiveHedge)

	cfg := policy.adaptiveHedge.cfg.Load()
	assert.InEpsilon(t, 0.99, cfg.percentile, 1e-9)
	assert.InEpsilon(t, 1.5, cfg.multiplier, 1e-9)
	assert.Equal(t, 5*time.Millisecond, cfg.floor)
	assert.Equal(t, int64(50), cfg.minSamples)
}

// ---------------------------------------------------------------------------
// Fuzz: compute clamping invariants
// ---------------------------------------------------------------------------

// FuzzAdaptiveHedgeCompute proves compute never panics and always returns a delay
// within [0, ceiling], at or above the floor once warm, for any tunables and
// percentile estimate.
func FuzzAdaptiveHedgeCompute(f *testing.F) {
	f.Add(int64(10_000_000), int64(50), 0.95, 1.0, int64(0), int64(time.Second), int64(20))
	f.Add(int64(0), int64(0), 0.5, 0.5, int64(time.Millisecond), int64(time.Hour), int64(5))

	f.Fuzz(func(t *testing.T,
		valueNanos, samples int64, pct, mult float64, floorN, ceilN, minS int64,
	) {
		// Keep inputs in realistic ranges: a non-negative percentile latency below
		// the sketch ceiling, a positive hedge ceiling, a non-negative floor.
		value := absInt64(valueNanos) % maxLatencyNanos
		ceiling := time.Duration(absInt64(ceilN)%maxLatencyNanos + 1)
		floor := time.Duration(absInt64(floorN) % maxLatencyNanos)
		sampleCount := absInt64(samples)

		clk := &stubClock{now: epochBase()}
		ah := newAdaptiveHedge(&adaptiveHedgeConfig{
			percentile: pct,
			multiplier: mult,
			floor:      floor,
			minSamples: minS,
		}, clk)

		// Seed the per-epoch cache so estimate returns the fuzzed value directly.
		ah.cachedValue.Store(value)
		ah.cachedSamples.Store(sampleCount)
		ah.cachedEpoch.Store(ah.window.epochOf(clk.Now()))

		got := ah.compute(ceiling)

		require.GreaterOrEqual(t, got, time.Duration(0), "delay never negative")
		require.LessOrEqual(t, got, ceiling, "ceiling is the hard maximum")

		cfg := ah.cfg.Load()
		if sampleCount < cfg.minSamples {
			require.Equal(t, ceiling, got, "below warmup the ceiling is used")

			return
		}

		if floor <= ceiling {
			require.GreaterOrEqual(t, got, floor, "floor is the lower bound")
		}
	})
}
