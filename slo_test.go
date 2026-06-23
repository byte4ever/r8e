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

// recordSLO folds total served outcomes into the governor's windows at the
// current clock epoch, the first failures of them as budget-burning errors.
func recordSLO(gov *SLOGovernor, total, failures int) {
	for i := range total {
		if i < failures {
			gov.Record(errBackend)

			continue
		}

		gov.Record(nil)
	}
}

// sloLongTotals returns the governor's current long-window served and failed
// counts under the lock.
func sloLongTotals(gov *SLOGovernor) (total, failures int64) {
	gov.mu.Lock()
	defer gov.mu.Unlock()

	return gov.long.sums(gov.clock.Now())
}

// ---------------------------------------------------------------------------
// Construction & clamping
// ---------------------------------------------------------------------------

func TestNewSLOGovernorDefaults(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.99, &stubClock{now: epochBase()}, &Hooks{})

	assert.InEpsilon(t, 0.99, gov.target, 1e-9)
	assert.Equal(t, defaultSLOLongWindow, gov.longWindowD)
	assert.Equal(t, defaultSLOShortWindow, gov.shortWindowD)
	assert.InEpsilon(t, defaultBurnThreshold, gov.burnThreshold, 1e-9)
	assert.InEpsilon(t, defaultMaxShedRate, gov.maxShedRate, 1e-9)
	assert.Equal(t, int64(defaultSLOMinRequests), gov.minRequests)
}

func TestNewSLOGovernorClampsInvalidParams(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(1.5, &stubClock{now: epochBase()}, &Hooks{},
		SLOLongWindow(-1),
		SLOShortWindow(-1),
		BurnThreshold(-1),
		MaxShedRate(2),
		SLOMinRequests(0),
	)

	assert.InEpsilon(t, defaultSLOTarget, gov.target, 1e-9, "out-of-range target")
	assert.Equal(t, defaultSLOLongWindow, gov.longWindowD)
	// A non-positive short window resets to a fraction of the long window.
	assert.Equal(t, defaultSLOLongWindow/sloShortLongRatio, gov.shortWindowD)
	assert.InEpsilon(t, defaultBurnThreshold, gov.burnThreshold, 1e-9)
	assert.InEpsilon(t, defaultMaxShedRate, gov.maxShedRate, 1e-9)
	assert.Equal(t, int64(defaultSLOMinRequests), gov.minRequests)
}

func TestNewSLOGovernorZeroTargetClamps(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0, &stubClock{now: epochBase()}, &Hooks{})

	assert.InEpsilon(t, defaultSLOTarget, gov.target, 1e-9, "target 0 is out of range")
}

func TestNewSLOGovernorKeepsValidParams(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.95, &stubClock{now: epochBase()}, &Hooks{},
		SLOLongWindow(30*time.Second),
		SLOShortWindow(3*time.Second),
		BurnThreshold(5),
		MaxShedRate(0.5),
		SLOMinRequests(7),
	)

	assert.InEpsilon(t, 0.95, gov.target, 1e-9)
	assert.Equal(t, 30*time.Second, gov.longWindowD)
	assert.Equal(t, 3*time.Second, gov.shortWindowD)
	assert.InEpsilon(t, 5.0, gov.burnThreshold, 1e-9)
	assert.InEpsilon(t, 0.5, gov.maxShedRate, 1e-9)
	assert.Equal(t, int64(7), gov.minRequests)
}

func TestSLOClampShortWindowNotShorterThanLong(t *testing.T) {
	t.Parallel()

	// A short window at least as long as the long one is reset to long/ratio.
	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOLongWindow(12*time.Second),
		SLOShortWindow(20*time.Second),
	)

	assert.Equal(t, time.Second, gov.shortWindowD, "12s/12 = 1s")
}

func TestSLOClampShortWindowFloor(t *testing.T) {
	t.Parallel()

	// A long window so small that long/ratio rounds to zero floors short at 1ns.
	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOLongWindow(1),
		SLOShortWindow(5),
	)

	assert.Equal(t, time.Duration(1), gov.shortWindowD)
}

func TestSLOBucketNanosFloorsAtOne(t *testing.T) {
	t.Parallel()

	assert.Equal(t, int64(1), sloBucketNanos(5), "5ns/10 floors to 1ns")
	assert.Equal(t, int64(1), sloBucketNanos(0))
}

// ---------------------------------------------------------------------------
// Burn rate & shed probability
// ---------------------------------------------------------------------------

func TestBurnRateZeroWhenNoSamples(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{})

	assert.Zero(t, gov.BurnRate())
}

func TestBurnRateMatchesFormula(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{})
	recordSLO(gov, 100, 30) // errorRate 0.30, budget 0.10 → burn 3.0

	assert.InEpsilon(t, 3.0, gov.BurnRate(), 1e-9)
}

func TestShedProbabilityBelowMinRequestsIsZero(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(20),
	)
	recordSLO(gov, 10, 10) // burn high but only 10 samples < 20

	assert.Zero(t, gov.ShedProbability())
}

func TestShedProbabilityZeroBelowThreshold(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	recordSLO(gov, 100, 10) // errorRate 0.10 → burn 1.0 < threshold 2.0

	assert.Zero(t, gov.ShedProbability())
}

func TestShedProbabilityMatchesFraction(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	recordSLO(gov, 100, 30) // burn 3.0 → 1 - 2/3 = 0.333…

	assert.InDelta(t, 1.0-2.0/3.0, gov.ShedProbability(), 1e-9)
}

func TestShedProbabilityCappedAtMax(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.99, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	recordSLO(gov, 100, 100) // errorRate 1.0, budget 0.01 → burn 100 → 0.98, capped 0.9

	assert.InEpsilon(t, defaultMaxShedRate, gov.ShedProbability(), 1e-9)
}

func TestShedProbabilityRequiresBothWindowsShortLow(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	gov := NewSLOGovernor(0.9, clk, &Hooks{},
		SLOLongWindow(10*time.Second),
		SLOShortWindow(time.Second),
		SLOMinRequests(5),
	)

	recordSLO(gov, 100, 100) // t0: all failures
	clk.now = clk.now.Add(2 * time.Second)
	recordSLO(gov, 100, 0) // t1: all successes — short window now sees only these

	// Long still spans both (100/200 → burn 5), but the short window is clean.
	assert.InEpsilon(t, 5.0, gov.BurnRate(), 1e-9, "long window still burning")
	assert.Zero(t, gov.ShedProbability(), "short window below threshold gates shedding")
}

func TestShedProbabilityRequiresBothWindowsLongLow(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	gov := NewSLOGovernor(0.9, clk, &Hooks{},
		SLOLongWindow(10*time.Second),
		SLOShortWindow(time.Second),
		SLOMinRequests(5),
	)

	recordSLO(gov, 200, 0) // t0: many clean successes
	clk.now = clk.now.Add(2 * time.Second)
	recordSLO(gov, 100, 30) // t1: short burn 3.0, but long dilutes to 30/300 → burn 1.0

	assert.InEpsilon(t, 1.0, gov.BurnRate(), 1e-9, "long window below threshold")
	assert.Zero(t, gov.ShedProbability(), "long window below threshold gates shedding")
}

func TestShedProbabilityScalesWithShortWindow(t *testing.T) {
	t.Parallel()

	// Both windows burn above the threshold but at DIFFERENT rates, so the test
	// can tell which window scales the shed probability. The doc promises the
	// short (current-intensity) window drives the magnitude.
	clk := &stubClock{now: epochBase()}
	gov := NewSLOGovernor(0.9, clk, &Hooks{},
		SLOLongWindow(10*time.Second),
		SLOShortWindow(time.Second),
		SLOMinRequests(5),
	)

	recordSLO(gov, 100, 100) // t0: all failures (ages out of the short window)
	clk.now = clk.now.Add(2 * time.Second)
	recordSLO(gov, 100, 50) // t1: 50% errors → short burn 5.0

	// short burn = 0.50/0.10 = 5.0; long burn = 150/200 / 0.10 = 7.5 (both > 2).
	// The shed probability must scale with the SHORT burn: 1 - 2/5 = 0.6.
	// A scale-by-long-window regression would yield 1 - 2/7.5 ≈ 0.733.
	assert.InDelta(t, 1.0-2.0/5.0, gov.ShedProbability(), 1e-9)
}

func TestShedProbabilityAtMinRequestsBoundary(t *testing.T) {
	t.Parallel()

	// Exactly minRequests samples must NOT be gated out (the gate is strict <),
	// so a high burn at the boundary sheds. A `<` → `<=` regression returns 0.
	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(20),
	)
	recordSLO(gov, 20, 20) // exactly 20 samples, all failures → burn 10

	assert.Positive(t, gov.ShedProbability(),
		"at exactly minRequests the short window is not gated out")
}

func TestShedProbabilityAtThresholdIsZero(t *testing.T) {
	t.Parallel()

	// A burn rate exactly at the threshold must not shed, through the live
	// ShedProbability path (not only the pure shedFraction unit). target 0.5 makes
	// the budget exactly 0.5, so 100% errors give a burn of exactly 2.0 with no
	// float drift (1-0.9 is NOT exactly 0.1, which would land just above).
	gov := NewSLOGovernor(0.5, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
		BurnThreshold(2.0),
	)
	recordSLO(gov, 100, 100) // errorRate 1.0 / budget 0.5 → burn exactly 2.0

	assert.Zero(t, gov.ShedProbability(), "burn at the threshold sheds nothing")
}

func TestShedFraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		burn      float64
		threshold float64
		maxShed   float64
		want      float64
	}{
		{"non-positive threshold", 10, 0, 0.9, 0},
		{"at threshold", 2, 2, 0.9, 0},
		{"below threshold", 1.5, 2, 0.9, 0},
		{"proportional", 4, 2, 0.9, 0.5},
		{"capped", 100, 2, 0.9, 0.9},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			assert.InDelta(t, tc.want, shedFraction(tc.burn, tc.threshold, tc.maxShed), 1e-9)
		})
	}
}

// ---------------------------------------------------------------------------
// Allow / admit
// ---------------------------------------------------------------------------

func TestSLOAllowForwardsWhenNoBurn(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{})
	gov.sampler = alwaysShed // would shed if probability were positive

	require.NoError(t, gov.Allow(context.Background()))
}

func TestSLOAllowShedsUnderBurn(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	recordSLO(gov, 100, 50) // burn 5.0
	gov.sampler = alwaysShed

	require.ErrorIs(t, gov.Allow(context.Background()), ErrSLOShed)
}

func TestSLOAllowForwardsWhenDrawAboveProbability(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	recordSLO(gov, 100, 30)                      // burn 3.0 → shed prob 0.333
	gov.sampler = func() float64 { return 0.95 } // draw above the probability

	require.NoError(t, gov.Allow(context.Background()))
}

func TestSLOAllowSheddabilityNeverBypasses(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	recordSLO(gov, 100, 100) // maximal burn
	gov.sampler = alwaysShed

	ctx := WithSheddability(context.Background(), SheddabilityNever)
	require.NoError(t, gov.Allow(ctx), "critical calls bypass shedding")
}

func TestSLOAllowSheddabilityAlwaysShedsWhenActive(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	recordSLO(gov, 100, 30) // shed prob 0.333 > 0
	gov.sampler = neverShed // irrelevant for SheddabilityAlways

	ctx := WithSheddability(context.Background(), SheddabilityAlways)
	require.ErrorIs(t, gov.Allow(ctx), ErrSLOShed, "sheddable dropped once shedding is active")
}

func TestSLOAllowSheddabilityAlwaysForwardsWhenIdle(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{})

	ctx := WithSheddability(context.Background(), SheddabilityAlways)
	require.NoError(t, gov.Allow(ctx), "no shedding active → sheddable still served")
}

func TestSLOShedCallNotRecorded(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	recordSLO(gov, 100, 50)
	gov.sampler = alwaysShed

	before, _ := sloLongTotals(gov)
	require.ErrorIs(t, gov.Allow(context.Background()), ErrSLOShed)
	after, _ := sloLongTotals(gov)

	assert.Equal(t, before, after, "a shed call must not be recorded")
}

// ---------------------------------------------------------------------------
// Record / isBurn / classifier
// ---------------------------------------------------------------------------

func TestSLORecordNilIsSuccess(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{})
	recordSLO(gov, 50, 0)

	total, failures := sloLongTotals(gov)
	assert.Equal(t, int64(50), total)
	assert.Zero(t, failures)
	assert.Zero(t, gov.BurnRate())
}

func TestSLORecordErrorBurns(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{})
	recordSLO(gov, 50, 20)

	_, failures := sloLongTotals(gov)
	assert.Equal(t, int64(20), failures)
}

func TestSLOClassifierExcludesError(t *testing.T) {
	t.Parallel()

	errBusiness := errors.New("not found")
	// Count only errBackend as a burn; treat errBusiness as a served success.
	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOClassifier(func(err error) bool { return errors.Is(err, errBackend) }),
	)

	for range 50 {
		gov.Record(errBusiness)
	}

	_, failures := sloLongTotals(gov)
	assert.Zero(t, failures, "classifier excludes business errors from burn")
	assert.Zero(t, gov.BurnRate())
}

// ---------------------------------------------------------------------------
// Window decay & edge cases
// ---------------------------------------------------------------------------

func TestSLOWindowDecaysOverTime(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	gov := NewSLOGovernor(0.9, clk, &Hooks{},
		SLOLongWindow(10*time.Second),
		SLOShortWindow(time.Second),
		SLOMinRequests(5),
	)
	recordSLO(gov, 100, 50)
	require.Positive(t, gov.ShedProbability(), "burning now")

	clk.now = clk.now.Add(11 * time.Second) // past the long window
	assert.Zero(t, gov.BurnRate(), "window decayed")
	assert.Zero(t, gov.ShedProbability())
}

func TestSLOWindowIgnoresFutureBucketAfterClockRewind(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	gov := NewSLOGovernor(0.9, clk, &Hooks{}, SLOLongWindow(10*time.Second))

	clk.now = clk.now.Add(20 * time.Second)
	recordSLO(gov, 10, 10) // bucket stamped at the later epoch

	clk.now = clk.now.Add(-20 * time.Second) // rewind: bucket is now in the future
	total, _ := sloLongTotals(gov)

	assert.Zero(t, total, "future-stamped bucket excluded")
}

func TestSLOBucketForHandlesNegativeEpoch(t *testing.T) {
	t.Parallel()

	// A pre-1970 instant yields a negative epoch; the ring index must fold back
	// into range and the burn math must still work. -7s gives a negative,
	// non-multiple-of-bucket epoch so the index-fold branch is exercised.
	clk := &stubClock{now: time.Unix(-7, 0)}
	gov := NewSLOGovernor(0.9, clk, &Hooks{})
	recordSLO(gov, 100, 30)

	assert.InEpsilon(t, 3.0, gov.BurnRate(), 1e-9)
}

func TestSLOShedding(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	assert.False(t, gov.Shedding())

	recordSLO(gov, 100, 50)
	assert.True(t, gov.Shedding())
}

// ---------------------------------------------------------------------------
// Reconfigure
// ---------------------------------------------------------------------------

func TestSLOReconfigureUpdatesParams(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{})
	gov.Reconfigure(0.95,
		SLOLongWindow(20*time.Second),
		BurnThreshold(4),
		MaxShedRate(0.7),
		SLOMinRequests(8),
	)

	assert.InEpsilon(t, 0.95, gov.target, 1e-9)
	assert.Equal(t, 20*time.Second, gov.longWindowD)
	assert.InEpsilon(t, 4.0, gov.burnThreshold, 1e-9)
	assert.InEpsilon(t, 0.7, gov.maxShedRate, 1e-9)
	assert.Equal(t, int64(8), gov.minRequests)
}

func TestSLOReconfigurePartialLeavesOthersUnchanged(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		BurnThreshold(5),
		SLOMinRequests(7),
	)
	gov.Reconfigure(0.9, MaxShedRate(0.5)) // touch only the cap

	assert.InEpsilon(t, 5.0, gov.burnThreshold, 1e-9, "threshold preserved")
	assert.Equal(t, int64(7), gov.minRequests, "min requests preserved")
	assert.InEpsilon(t, 0.5, gov.maxShedRate, 1e-9, "cap updated")
}

func TestSLOReconfigureWindowResetsHistory(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOLongWindow(10*time.Second),
	)
	recordSLO(gov, 100, 50)
	require.Positive(t, gov.BurnRate())

	gov.Reconfigure(0.9, SLOLongWindow(30*time.Second)) // different bucket size
	assert.Zero(t, gov.BurnRate(), "window resize clears history")

	// Fresh records after the resize accumulate cleanly in the new window, and
	// the pre-resize history does not resurface into the burn rate.
	recordSLO(gov, 100, 20) // errorRate 0.20 / budget 0.10 → burn 2.0
	assert.InEpsilon(t, 2.0, gov.BurnRate(), 1e-9,
		"post-resize window reflects only the fresh records")
}

func TestSLOReconfigureSameWindowKeepsHistory(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOLongWindow(10*time.Second),
	)
	recordSLO(gov, 100, 50)

	gov.Reconfigure(0.9, BurnThreshold(3)) // no window change
	assert.Positive(t, gov.BurnRate(), "unchanged window keeps history")
}

func TestSLOReconfigurePreservesClassifier(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOClassifier(func(error) bool { return false }),
	)
	gov.Reconfigure(0.9, BurnThreshold(3)) // classifier untouched

	for range 50 {
		gov.Record(errBackend)
	}

	assert.Zero(t, gov.BurnRate(), "classifier survived reconfigure")
}

func TestSLOReconfigureTargetChangesBurn(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{})
	recordSLO(gov, 100, 10) // errorRate 0.10

	assert.InEpsilon(t, 1.0, gov.BurnRate(), 1e-9, "0.10 / 0.10")

	gov.Reconfigure(0.95) // budget shrinks to 0.05 → same errors burn twice as fast
	assert.InEpsilon(t, 2.0, gov.BurnRate(), 1e-9, "0.10 / 0.05")
}

// ---------------------------------------------------------------------------
// Policy integration
// ---------------------------------------------------------------------------

func TestPolicySLOForwardsHealthy(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	p := NewPolicy[int]("svc", WithClock(clk), WithSLO(0.9))

	got, err := p.Do(context.Background(), func(context.Context) (int, error) {
		return 7, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 7, got)
}

func TestPolicySLOShedsCall(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	p := NewPolicy[int]("svc", WithClock(clk),
		WithSLO(0.9, SLOLongWindow(10*time.Second), SLOShortWindow(time.Second), SLOMinRequests(5)),
	)

	// Build a burn through the normal path with shedding disabled.
	p.slo.sampler = neverShed

	for range 50 {
		_, err := p.Do(context.Background(), func(context.Context) (int, error) {
			return 0, errBackend
		})
		require.ErrorIs(t, err, errBackend)
	}

	// Now shedding is active: the next default call is shed before the function.
	p.slo.sampler = alwaysShed

	called := false
	_, err := p.Do(context.Background(), func(context.Context) (int, error) {
		called = true

		return 0, nil
	})

	require.ErrorIs(t, err, ErrSLOShed)
	assert.False(t, called, "shed call never reaches the function")
}

func TestPolicySLOMetricsAndHealth(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	p := NewPolicy[int]("svc", WithClock(clk),
		WithSLO(0.9, SLOLongWindow(10*time.Second), SLOShortWindow(time.Second), SLOMinRequests(5)),
	)

	p.slo.sampler = neverShed
	for range 50 {
		_, _ = p.Do(context.Background(), func(context.Context) (int, error) {
			return 0, errBackend
		})
	}

	p.slo.sampler = alwaysShed
	_, err := p.Do(context.Background(), func(context.Context) (int, error) {
		return 0, nil
	})
	require.ErrorIs(t, err, ErrSLOShed)

	m := p.Metrics()
	assert.Equal(t, int64(1), m.SLOShed, "one shed recorded")
	assert.Positive(t, m.SLOBurnRate, "burn rate surfaced")
	assert.Positive(t, m.SLOShedProbability, "shed probability surfaced")

	health := p.HealthStatus()
	assert.Equal(t, CriticalityDegraded, health.Criticality)
	assert.Contains(t, health.Conditions, ConditionSLOBurning)
}

func TestPolicySLOClampsBadTarget(t *testing.T) {
	t.Parallel()

	// An out-of-range target must clamp, not panic, at construction.
	require.NotPanics(t, func() {
		_ = NewPolicy[int]("svc", WithSLO(1.5))
	})
}

// ---------------------------------------------------------------------------
// Config (BuildOptions / Reconfigure)
// ---------------------------------------------------------------------------

func TestBuildOptionsSLO(t *testing.T) {
	t.Parallel()

	target := 0.95
	threshold := 3.0
	long := "20s"
	short := "2s"
	minReq := 9
	shedCap := 0.8

	opts, err := BuildOptions(&PolicyConfig{
		SLO: &SLOConfig{
			Target:        &target,
			LongWindow:    &long,
			ShortWindow:   &short,
			BurnThreshold: &threshold,
			MaxShedRate:   &shedCap,
			MinRequests:   &minReq,
		},
	})
	require.NoError(t, err)

	p := NewPolicy[int]("svc", append(opts, WithClock(&stubClock{now: epochBase()}))...)
	require.NotNil(t, p.slo)
	assert.InEpsilon(t, 0.95, p.slo.target, 1e-9)
	assert.Equal(t, 20*time.Second, p.slo.longWindowD)
	assert.InEpsilon(t, 3.0, p.slo.burnThreshold, 1e-9)
}

func TestBuildOptionsSLOTargetRequired(t *testing.T) {
	t.Parallel()

	_, err := BuildOptions(&PolicyConfig{SLO: &SLOConfig{}})
	require.ErrorIs(t, err, ErrSLOTargetRequired)
}

func TestBuildOptionsSLOBadWindow(t *testing.T) {
	t.Parallel()

	target := 0.99
	bad := "not-a-duration"

	_, err := BuildOptions(&PolicyConfig{
		SLO: &SLOConfig{Target: &target, LongWindow: &bad},
	})
	require.Error(t, err)
	assert.ErrorContains(t, err, "slo.long_window")
	assert.ErrorContains(t, err, "not-a-duration",
		"the offending value survives in the wrapped error")
}

func TestReconfigureSLO(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	p := NewPolicy[int]("svc", WithClock(clk), WithSLO(0.9))

	target := 0.95
	threshold := 4.0

	require.NoError(t, p.Reconfigure(PolicyConfig{
		SLO: &SLOConfig{Target: &target, BurnThreshold: &threshold},
	}))

	assert.InEpsilon(t, 0.95, p.slo.target, 1e-9)
	assert.InEpsilon(t, 4.0, p.slo.burnThreshold, 1e-9)
}

func TestReconfigureSLOAbsent(t *testing.T) {
	t.Parallel()

	p := NewPolicy[int]("svc", WithRetry(2, ConstantBackoff(0)))

	target := 0.9
	err := p.Reconfigure(PolicyConfig{SLO: &SLOConfig{Target: &target}})

	require.ErrorIs(t, err, ErrPatternAbsent)
}

func TestReconfigureSLOTargetRequired(t *testing.T) {
	t.Parallel()

	p := NewPolicy[int]("svc", WithSLO(0.9))

	err := p.Reconfigure(PolicyConfig{SLO: &SLOConfig{}})
	require.ErrorIs(t, err, ErrSLOTargetRequired)
}

func TestReconfigureSLOBadWindow(t *testing.T) {
	t.Parallel()

	p := NewPolicy[int]("svc", WithSLO(0.9))

	target := 0.9
	bad := "nope"
	err := p.Reconfigure(PolicyConfig{
		SLO: &SLOConfig{Target: &target, ShortWindow: &bad},
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "slo.short_window")
}

// ---------------------------------------------------------------------------
// Concurrency (run with -race)
// ---------------------------------------------------------------------------

func TestSLOConcurrentAccess(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	gov.sampler = neverShed // deterministic; set before launching goroutines

	var wg sync.WaitGroup

	for range 16 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 100 {
				_ = gov.Allow(context.Background())
				gov.Record(errBackend)
				_ = gov.BurnRate()
				_ = gov.ShedProbability()
			}
		}()
	}

	wg.Wait()

	// The clock never advances, so every one of the 16×100 recorded failures
	// lands in the same epoch: a lost update under contention (which -race alone
	// would not catch) would leave the totals short of 1600.
	total, failures := sloLongTotals(gov)
	assert.Equal(t, int64(1600), total, "every recorded call counted")
	assert.Equal(t, int64(1600), failures, "every recorded failure counted")
}

func TestSLOConcurrentReconfigure(t *testing.T) {
	t.Parallel()

	gov := NewSLOGovernor(0.9, &stubClock{now: epochBase()}, &Hooks{},
		SLOMinRequests(5),
	)
	gov.sampler = neverShed

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		for range 200 {
			gov.Reconfigure(0.95, BurnThreshold(3), SLOMinRequests(10))
		}
	}()

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 200 {
				_ = gov.Allow(context.Background())
				gov.Record(nil)
			}
		}()
	}

	wg.Wait()

	// Reconfigure here never changes the window size, so it never clears history;
	// all 8×200 successful records must survive concurrent reconfiguration. A
	// lost update would leave the total short of 1600.
	total, failures := sloLongTotals(gov)
	assert.Equal(t, int64(1600), total, "every recorded call survived reconfigure")
	assert.Zero(t, failures, "all records were successes")
}

// ---------------------------------------------------------------------------
// Fuzz
// ---------------------------------------------------------------------------

func FuzzSLOShedFraction(f *testing.F) {
	f.Add(3.0, 2.0, 0.9)
	f.Add(0.0, 0.0, 0.0)
	f.Add(1e18, 1.0, 1.0)
	f.Add(-1.0, -1.0, -1.0)

	f.Fuzz(func(t *testing.T, burn, threshold, maxShed float64) {
		got := shedFraction(burn, threshold, maxShed)

		require.False(t, math.IsNaN(got), "shed fraction must never be NaN")
		require.GreaterOrEqual(t, got, 0.0, "shed fraction is non-negative")

		if maxShed >= 0 {
			require.LessOrEqual(t, got, maxShed, "shed fraction never exceeds the cap")
		}
	})
}
