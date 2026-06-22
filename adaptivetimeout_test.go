package r8e

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// adaptiveTimeout — config resolution
// ---------------------------------------------------------------------------

// TestAdaptiveTimeoutResolveDefaults proves every out-of-range or unset tunable
// falls back to its documented default rather than being rejected.
func TestAdaptiveTimeoutResolveDefaults(t *testing.T) {
	t.Parallel()

	cfg := adaptiveTimeoutConfig{} // all zero
	cfg.resolve()

	assert.InEpsilon(t, defaultAdaptiveTimeoutPercentile, cfg.percentile, 1e-9)
	assert.InEpsilon(t, defaultAdaptiveTimeoutMultiplier, cfg.multiplier, 1e-9)
	assert.Equal(t, int64(defaultAdaptiveTimeoutMinSamples), cfg.minSamples)
	assert.Equal(t, time.Duration(0), cfg.floor)

	// Out-of-range values reset; a negative floor disables the floor.
	bad := adaptiveTimeoutConfig{percentile: 1.5, multiplier: 0.5, minSamples: -3, floor: -time.Second}
	bad.resolve()

	assert.InEpsilon(t, defaultAdaptiveTimeoutPercentile, bad.percentile, 1e-9)
	assert.InEpsilon(t, defaultAdaptiveTimeoutMultiplier, bad.multiplier, 1e-9)
	assert.Equal(t, int64(defaultAdaptiveTimeoutMinSamples), bad.minSamples)
	assert.Equal(t, time.Duration(0), bad.floor)

	// In-range values are preserved.
	good := adaptiveTimeoutConfig{percentile: 0.95, multiplier: 3, minSamples: 50, floor: 5 * time.Millisecond}
	good.resolve()

	assert.InEpsilon(t, 0.95, good.percentile, 1e-9)
	assert.InEpsilon(t, 3.0, good.multiplier, 1e-9)
	assert.Equal(t, int64(50), good.minSamples)
	assert.Equal(t, 5*time.Millisecond, good.floor)
}

// ---------------------------------------------------------------------------
// adaptiveTimeout — compute (warmup, percentile, clamping)
// ---------------------------------------------------------------------------

// newTestAdaptiveTimeout builds an adaptiveTimeout on a fixed-instant stub clock
// and records n successful samples of duration d into its window, all in one
// epoch.
func newTestAdaptiveTimeout(cfg *adaptiveTimeoutConfig, d time.Duration, n int) (*adaptiveTimeout, *stubClock) {
	clk := &stubClock{now: epochBase()}
	at := newAdaptiveTimeout(cfg, clk)

	for range n {
		at.record(d, nil)
	}

	return at, clk
}

// TestAdaptiveTimeoutCompute table-drives the warmup gate and the floor/ceiling
// clamp. Every case records samples of 10ms, so once warm the raw estimate is
// p99×multiplier ≈ 20ms (default p99×2); the cases then probe each clamp edge.
func TestAdaptiveTimeoutCompute(t *testing.T) {
	t.Parallel()

	const sample = 10 * time.Millisecond

	tests := map[string]struct {
		cfg     adaptiveTimeoutConfig
		samples int
		ceiling time.Duration
		want    time.Duration // exact when clamped; 0 ⇒ assert ~20ms within accuracy
	}{
		"warmup below minSamples uses ceiling": {
			cfg:     adaptiveTimeoutConfig{minSamples: 20},
			samples: 5,
			ceiling: time.Second,
			want:    time.Second,
		},
		"warm estimate is p99 times multiplier": {
			samples: 50,
			ceiling: time.Second,
			want:    0, // ~20ms, asserted within the DDSketch accuracy bound
		},
		"raw estimate above ceiling clamps to ceiling": {
			samples: 50,
			ceiling: 15 * time.Millisecond,
			want:    15 * time.Millisecond,
		},
		"raw estimate below floor lifts to floor": {
			cfg:     adaptiveTimeoutConfig{floor: 100 * time.Millisecond},
			samples: 50,
			ceiling: time.Second,
			want:    100 * time.Millisecond,
		},
		"ceiling wins even when floor exceeds it": {
			cfg:     adaptiveTimeoutConfig{floor: time.Second},
			samples: 50,
			ceiling: 50 * time.Millisecond,
			want:    50 * time.Millisecond,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := tc.cfg
			at, _ := newTestAdaptiveTimeout(&cfg, sample, tc.samples)

			got := at.compute(tc.ceiling)
			if tc.want == 0 {
				assert.Less(t, relErr(got, 20*time.Millisecond), 0.03,
					"want ~20ms (p99×2), got %s", got)

				return
			}

			assert.Equal(t, tc.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// adaptiveTimeout — record only feeds successes
// ---------------------------------------------------------------------------

// TestAdaptiveTimeoutRecordOnlySuccess proves a failed or timed-out call is not
// folded into the percentile window, so a timeout cannot inflate its own value.
func TestAdaptiveTimeoutRecordOnlySuccess(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	at := newAdaptiveTimeout(&adaptiveTimeoutConfig{}, clk)

	for range 25 {
		at.record(10*time.Millisecond, nil)
	}

	_, samples := at.window.quantileSnapshot(0.99)
	require.Equal(t, int64(25), samples)

	// Failures (including a slow timeout) must not change the window.
	for range 25 {
		at.record(time.Second, errors.New("boom")) //nolint:err113 // test sentinel
		at.record(time.Second, ErrTimeout)
	}

	value, after := at.window.quantileSnapshot(0.99)
	assert.Equal(t, int64(25), after, "failures must not be recorded")
	assert.Less(t, relErr(value, 10*time.Millisecond), 0.03,
		"p99 stays at the success latency, got %s", value)
}

// ---------------------------------------------------------------------------
// adaptiveTimeout — per-epoch estimate cache
// ---------------------------------------------------------------------------

// TestAdaptiveTimeoutEstimateCachedPerEpoch proves the expensive percentile read
// is memoized within one window epoch and refreshed when the clock advances past
// it, keeping the per-call path off the ring merge.
func TestAdaptiveTimeoutEstimateCachedPerEpoch(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	at := newAdaptiveTimeout(&adaptiveTimeoutConfig{}, clk)

	for range 25 {
		at.record(10*time.Millisecond, nil)
	}

	v1, s1 := at.estimate(0.99)
	require.Equal(t, int64(25), s1)

	// More samples in the SAME epoch must not change the cached estimate.
	for range 25 {
		at.record(100*time.Millisecond, nil)
	}

	v2, s2 := at.estimate(0.99)
	assert.Equal(t, s1, s2, "sample count is cached within the epoch")
	assert.Equal(t, v1, v2, "percentile is cached within the epoch")

	// Advancing the clock a full epoch refreshes against all live samples.
	clk.now = epochBase().Add(defaultLatencyWindow / latencyWindowBuckets)

	v3, s3 := at.estimate(0.99)
	assert.Equal(t, int64(50), s3, "refresh sees every live sample")
	assert.Greater(t, v3, v1, "p99 rises once the slow samples are merged in")
}

// TestAdaptiveTimeoutEstimateConcurrentRefresh releases a herd of estimators at a
// fresh epoch: the cache starts invalid (sentinel epoch), so all of them miss the
// fast path and pile onto refreshMu. Exactly one merges the ring; every other,
// re-checking under the lock, takes the double-check early-return — they must all
// agree on the same merged estimate. This exercises the thundering-herd guard
// (the re-check under refreshMu) and the refresh path together under -race.
func TestAdaptiveTimeoutEstimateConcurrentRefresh(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	at := newAdaptiveTimeout(&adaptiveTimeoutConfig{}, clk)

	for range 30 {
		at.record(10*time.Millisecond, nil)
	}

	const herd = 64

	var (
		wg      sync.WaitGroup
		start   = make(chan struct{})
		results = make([]time.Duration, herd)
		samples = make([]int64, herd)
	)

	for i := range herd {
		wg.Add(1)

		go func() {
			defer wg.Done()
			<-start

			results[i], samples[i] = at.estimate(0.99)
		}()
	}

	close(start) // release the herd simultaneously
	wg.Wait()

	// One merge, the rest served from the cache — all identical and non-zero.
	for i := range herd {
		assert.Equal(t, results[0], results[i], "estimate %d disagreed", i)
		assert.Equal(t, int64(30), samples[i], "estimate %d saw wrong sample count", i)
	}

	assert.Positive(t, results[0])
}

// ---------------------------------------------------------------------------
// adaptiveTimeout — reconfigure
// ---------------------------------------------------------------------------

// TestAdaptiveTimeoutReconfigurePreservesUnset proves a runtime overlay keeps the
// fields it does not set and resets a field given an out-of-range value.
func TestAdaptiveTimeoutReconfigurePreservesUnset(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	at := newAdaptiveTimeout(&adaptiveTimeoutConfig{
		percentile: 0.95,
		multiplier: 3,
		floor:      50 * time.Millisecond,
		minSamples: 10,
	}, clk)

	at.reconfigure(AdaptiveTimeoutMultiplier(4))

	cfg := at.cfg.Load()
	assert.InEpsilon(t, 0.95, cfg.percentile, 1e-9, "percentile preserved")
	assert.InEpsilon(t, 4.0, cfg.multiplier, 1e-9, "multiplier updated")
	assert.Equal(t, 50*time.Millisecond, cfg.floor, "floor preserved")
	assert.Equal(t, int64(10), cfg.minSamples, "minSamples preserved")

	// An explicit out-of-range value resets that field to its default.
	at.reconfigure(AdaptiveTimeoutPercentile(0))

	cfg = at.cfg.Load()
	assert.InEpsilon(t, defaultAdaptiveTimeoutPercentile, cfg.percentile, 1e-9)
	assert.InEpsilon(t, 4.0, cfg.multiplier, 1e-9, "other fields still preserved")
}

// ---------------------------------------------------------------------------
// Policy integration
// ---------------------------------------------------------------------------

// TestWithTimeoutAdaptiveMetrics drives a policy whose calls each measure 10ms and
// proves Metrics().AdaptiveTimeout reports the adapted value (p99×2) once warm,
// while the timeout never actually fires.
func TestWithTimeoutAdaptiveMetrics(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase(), elapsed: 10 * time.Millisecond}
	policy := NewPolicy[string](
		"adaptive",
		WithClock(clk),
		WithRegistry(NewRegistry()),
		WithTimeout(time.Second, AdaptiveTimeout()),
	)

	fast := func(_ context.Context) (string, error) { return "ok", nil }

	for range 30 {
		_, err := policy.Do(context.Background(), fast)
		require.NoError(t, err)
	}

	// Advance one epoch so the per-epoch estimate refreshes against the 30 samples.
	clk.now = epochBase().Add(defaultLatencyWindow / latencyWindowBuckets)

	metrics := policy.Metrics()
	assert.Less(t, relErr(metrics.AdaptiveTimeout, 20*time.Millisecond), 0.03,
		"want ~20ms (p99×2), got %s", metrics.AdaptiveTimeout)
	assert.Zero(t, metrics.Timeouts, "no call exceeded the ceiling")
}

// TestWithTimeoutFixedReportsNoAdaptiveTimeout proves a plain WithTimeout builds a
// non-adaptive timeout, leaving the AdaptiveTimeout gauge at zero.
func TestWithTimeoutFixedReportsNoAdaptiveTimeout(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string](
		"fixed",
		WithRegistry(NewRegistry()),
		WithTimeout(time.Second),
	)

	assert.Nil(t, policy.adaptiveTimeout)
	assert.Zero(t, policy.Metrics().AdaptiveTimeout)
}

// TestAdaptiveTimeoutMiddlewareFiresAndExcludesTimeout drives a real call past the
// timeout through the policy chain: it must return ErrTimeout, count a timeout, and
// crucially NOT record the timed-out latency into the adaptive window (a timeout
// must never inflate the percentile that set it). Uses the real clock with a wide
// margin (20ms ceiling vs a 500ms call) to stay deterministic.
func TestAdaptiveTimeoutMiddlewareFiresAndExcludesTimeout(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string](
		"adaptive-fires",
		WithRegistry(NewRegistry()),
		WithTimeout(20*time.Millisecond, AdaptiveTimeout()),
	)

	slow := func(ctx context.Context) (string, error) {
		select {
		case <-time.After(500 * time.Millisecond):
			return "late", nil
		case <-ctx.Done():
			return "", ctx.Err() //nolint:wrapcheck // surfacing the cancellation
		}
	}

	_, err := policy.Do(context.Background(), slow)
	require.ErrorIs(t, err, ErrTimeout)

	assert.Equal(t, int64(1), policy.Metrics().Timeouts)

	// The timed-out call must not feed the adaptive window.
	_, samples := policy.adaptiveTimeout.window.quantileSnapshot(0.99)
	assert.Zero(t, samples, "a timed-out call must not be recorded")
}

// TestPolicyReconfigureAdaptiveTimeout proves the adaptive tunables hot-reload via
// the adaptive_timeout config block, and that targeting a non-adaptive timeout is
// rejected.
func TestPolicyReconfigureAdaptiveTimeout(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	policy := NewPolicy[string](
		"adaptive",
		WithClock(clk),
		WithRegistry(NewRegistry()),
		WithTimeout(time.Second, AdaptiveTimeout()),
	)

	err := policy.Reconfigure(PolicyConfig{
		AdaptiveTimeout: &AdaptiveTimeoutConfig{Multiplier: f64Ptr(5)},
	})
	require.NoError(t, err)
	assert.InEpsilon(t, 5.0, policy.adaptiveTimeout.cfg.Load().multiplier, 1e-9)

	// A bad floor string surfaces a parse error naming the offending field.
	err = policy.Reconfigure(PolicyConfig{
		AdaptiveTimeout: &AdaptiveTimeoutConfig{Floor: strPtr("nope")},
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "adaptive_timeout.floor")

	// A bad ceiling (timeout) duration string surfaces a parse error too.
	err = policy.Reconfigure(PolicyConfig{Timeout: strPtr("nope")})
	require.Error(t, err)
	assert.ErrorContains(t, err, "reconfigure timeout")

	// The ceiling itself reconfigures through the timeout field.
	require.NoError(t, policy.Reconfigure(PolicyConfig{Timeout: strPtr("2s")}))
	assert.Equal(t, int64(2*time.Second), policy.timeout.Load())

	// A non-adaptive timeout cannot have adaptation enabled at runtime.
	fixed := NewPolicy[string]("fixed", WithRegistry(NewRegistry()), WithTimeout(time.Second))
	err = fixed.Reconfigure(PolicyConfig{
		AdaptiveTimeout: &AdaptiveTimeoutConfig{Multiplier: f64Ptr(3)},
	})
	require.ErrorIs(t, err, ErrAdaptiveTimeoutWithoutTimeout)
}

// TestAdaptiveTimeoutReconfigurePercentileTakesEffectImmediately proves a
// percentile reconfigure is not masked by the per-epoch estimate cache: the cache
// is invalidated on reconfigure, so the new percentile drives compute on the very
// next call within the same window epoch (not only after the epoch rolls over).
func TestAdaptiveTimeoutReconfigurePercentileTakesEffectImmediately(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	at := newAdaptiveTimeout(&adaptiveTimeoutConfig{percentile: 0.50}, clk)

	// A skewed distribution: most calls fast (~5ms), a slow tail (~80ms) so p50 and
	// p99 are far apart and a percentile change is observable.
	for range 90 {
		at.record(5*time.Millisecond, nil)
	}

	for range 10 {
		at.record(80*time.Millisecond, nil)
	}

	low := at.compute(time.Second) // p50×2 ≈ 10ms, caches this epoch

	at.reconfigure(AdaptiveTimeoutPercentile(0.99))

	high := at.compute(time.Second) // same epoch — must already reflect p99
	assert.Greater(t, high, 3*low,
		"p99×2 must dominate p50×2 immediately after reconfigure (got %s vs %s)", high, low)
}

// TestBuildOptionsAdaptiveTimeout proves the config path wires the adaptive
// tunables, rejects an adaptive block without a timeout, and reports a bad floor.
func TestBuildOptionsAdaptiveTimeout(t *testing.T) {
	t.Parallel()

	// adaptive_timeout without timeout is rejected the same cold as hot.
	_, err := BuildOptions(&PolicyConfig{
		AdaptiveTimeout: &AdaptiveTimeoutConfig{Multiplier: f64Ptr(2)},
	})
	require.ErrorIs(t, err, ErrAdaptiveTimeoutWithoutTimeout)

	// A bad floor string fails to parse, naming the offending field.
	_, err = BuildOptions(&PolicyConfig{
		Timeout:         strPtr("1s"),
		AdaptiveTimeout: &AdaptiveTimeoutConfig{Floor: strPtr("nope")},
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "adaptive_timeout.floor")

	// A valid block builds a working adaptive policy.
	opts, err := BuildOptions(&PolicyConfig{
		Timeout: strPtr("1s"),
		AdaptiveTimeout: &AdaptiveTimeoutConfig{
			Percentile: f64Ptr(0.95),
			Multiplier: f64Ptr(3),
			Floor:      strPtr("5ms"),
			MinSamples: intPtr(50),
		},
	})
	require.NoError(t, err)

	policy := NewPolicy[string]("cfg", append(opts, WithRegistry(NewRegistry()))...)
	require.NotNil(t, policy.adaptiveTimeout)

	cfg := policy.adaptiveTimeout.cfg.Load()
	assert.InEpsilon(t, 0.95, cfg.percentile, 1e-9)
	assert.InEpsilon(t, 3.0, cfg.multiplier, 1e-9)
	assert.Equal(t, 5*time.Millisecond, cfg.floor)
	assert.Equal(t, int64(50), cfg.minSamples)
}

// ---------------------------------------------------------------------------
// Fuzz: compute clamping invariants
// ---------------------------------------------------------------------------

// FuzzAdaptiveTimeoutCompute proves compute never panics and always returns a
// timeout within [0, ceiling], at or above the floor once warm, for any tunables
// and percentile estimate.
func FuzzAdaptiveTimeoutCompute(f *testing.F) {
	f.Add(int64(10_000_000), int64(50), 0.99, 2.0, int64(0), int64(time.Second), int64(20))
	f.Add(int64(0), int64(0), 0.5, 1.0, int64(time.Millisecond), int64(time.Hour), int64(5))

	f.Fuzz(func(t *testing.T,
		valueNanos, samples int64, pct, mult float64, floorN, ceilN, minS int64,
	) {
		// Keep inputs in realistic ranges: a non-negative percentile latency below
		// the sketch ceiling, a positive timeout ceiling, a non-negative floor.
		value := absInt64(valueNanos) % maxLatencyNanos
		ceiling := time.Duration(absInt64(ceilN)%maxLatencyNanos + 1)
		floor := time.Duration(absInt64(floorN) % maxLatencyNanos)
		sampleCount := absInt64(samples)

		clk := &stubClock{now: epochBase()}
		at := newAdaptiveTimeout(&adaptiveTimeoutConfig{
			percentile: pct,
			multiplier: mult,
			floor:      floor,
			minSamples: minS,
		}, clk)

		// Seed the per-epoch cache so estimate returns the fuzzed value directly.
		at.cachedValue.Store(value)
		at.cachedSamples.Store(sampleCount)
		at.cachedEpoch.Store(at.window.epochOf(clk.Now()))

		got := at.compute(ceiling)

		require.GreaterOrEqual(t, got, time.Duration(0), "timeout never negative")
		require.LessOrEqual(t, got, ceiling, "ceiling is the hard maximum")

		cfg := at.cfg.Load()
		if sampleCount < cfg.minSamples {
			require.Equal(t, ceiling, got, "below warmup the ceiling is used")

			return
		}

		if floor <= ceiling {
			require.GreaterOrEqual(t, got, floor, "floor is the lower bound")
		}
	})
}

// absInt64 returns the absolute value of n, mapping math.MinInt64 to 0 to avoid
// the overflow of negating it.
func absInt64(n int64) int64 {
	if n < 0 {
		if n == math.MinInt64 {
			return 0
		}

		return -n
	}

	return n
}
