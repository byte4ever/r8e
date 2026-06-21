package r8e

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errBackend is a generic downstream failure used to widen the request/accept
// gap in throttler tests.
var errBackend = errors.New("backend overloaded")

// neverShed makes Allow forward every admitted call (sampler 1.0 is never below
// any probability), so a test can populate the window deterministically through
// the normal admission path without local shedding interfering.
func neverShed() float64 { return 1.0 }

// alwaysShed makes Allow shed whenever the probability is above zero (sampler 0.0
// is below any positive probability).
func alwaysShed() float64 { return 0.0 }

// forward drives one admitted call through the throttler with local shedding
// disabled, recording it as accepted or rejected by the backend.
func forward(t *testing.T, th *Throttler, accepted bool) {
	t.Helper()

	th.sampler = neverShed
	require.NoError(t, th.Allow(), "call should be admitted with shedding off")

	if accepted {
		th.Record(nil)

		return
	}

	th.Record(errBackend)
}

// feed runs requests calls through the throttler, accepting the first accepts of
// them and rejecting the rest, all within the current clock epoch.
func feed(t *testing.T, th *Throttler, requests, accepts int) {
	t.Helper()

	for i := range requests {
		forward(t, th, i < accepts)
	}
}

// ---------------------------------------------------------------------------
// Construction & clamping
// ---------------------------------------------------------------------------

func TestNewThrottlerDefaults(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{}, &Hooks{})

	assert.InEpsilon(t, defaultOverloadRatio, th.overloadRatio, 1e-9)
	assert.InEpsilon(t, defaultMaxRejectionRate, th.maxRejectionRate, 1e-9)
	assert.Equal(t, int64(defaultMinRequests), th.minRequests)
	assert.Equal(t, defaultThrottleWindow, th.window)
	assert.Nil(t, th.classifier)
}

func TestNewThrottlerClampsInvalidParams(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{}, &Hooks{},
		OverloadRatio(0.5),           // < 1 → default
		MaxRejectionRate(1.5),        // > 1 → default
		ThrottleWindow(-time.Second), // <= 0 → default
		MinRequests(0),               // < 1 → default
	)

	assert.InEpsilon(t, defaultOverloadRatio, th.overloadRatio, 1e-9)
	assert.InEpsilon(t, defaultMaxRejectionRate, th.maxRejectionRate, 1e-9)
	assert.Equal(t, defaultThrottleWindow, th.window)
	assert.Equal(t, int64(defaultMinRequests), th.minRequests)
}

func TestNewThrottlerKeepsValidParams(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{}, &Hooks{},
		OverloadRatio(3),
		MaxRejectionRate(0.5),
		ThrottleWindow(30*time.Second),
		MinRequests(5),
	)

	assert.InEpsilon(t, 3.0, th.overloadRatio, 1e-9)
	assert.InEpsilon(t, 0.5, th.maxRejectionRate, 1e-9)
	assert.Equal(t, 30*time.Second, th.window)
	assert.Equal(t, int64(5), th.minRequests)
}

func TestMaxRejectionRateZeroResetsToDefault(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{}, &Hooks{}, MaxRejectionRate(0))

	assert.InEpsilon(t, defaultMaxRejectionRate, th.maxRejectionRate, 1e-9)
}

func TestBucketNanosForFloorsAtOne(t *testing.T) {
	t.Parallel()

	// A window smaller than the bucket count divides to zero nanos and must be
	// floored to 1 so the epoch division never divides by zero.
	assert.Equal(t, int64(1), bucketNanosFor(throttleBuckets-1))
	assert.Equal(t, int64(1), bucketNanosFor(throttleBuckets))
	assert.Equal(t, int64(2), bucketNanosFor(2*throttleBuckets))
}

// ---------------------------------------------------------------------------
// Rejection probability — the SRE formula
// ---------------------------------------------------------------------------

func TestRejectionProbabilityBelowMinRequestsIsZero(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{}, MinRequests(10))

	feed(t, th, 9, 0) // 9 requests, all rejected, but below the 10 minimum

	assert.Zero(t, th.RejectionProbability())
}

func TestRejectionProbabilityZeroWhenWithinRatio(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{}, MinRequests(1))

	// 10 requests, 5 accepts: requests == K*accepts, so the gap is zero.
	feed(t, th, 10, 5)

	assert.Zero(t, th.RejectionProbability())
}

func TestRejectionProbabilityMatchesFormula(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{}, MinRequests(1))

	// 10 requests, 3 accepts, K=2: (10 - 6) / (10 + 1) = 4/11.
	feed(t, th, 10, 3)

	assert.InEpsilon(t, 4.0/11.0, th.RejectionProbability(), 1e-9)
}

func TestRejectionProbabilityCappedAtMax(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{},
		MinRequests(1), MaxRejectionRate(0.9))

	// 10 requests, 0 accepts: raw 10/11 ≈ 0.909, capped to 0.9.
	feed(t, th, 10, 0)

	assert.InEpsilon(t, 0.9, th.RejectionProbability(), 1e-9)
}

// ---------------------------------------------------------------------------
// Admission decision
// ---------------------------------------------------------------------------

func TestAllowForwardsWhenHealthy(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{}, MinRequests(1))

	th.sampler = alwaysShed // would shed if probability were positive

	// No window history yet → probability 0 → always admitted.
	assert.NoError(t, th.Allow())
}

func TestAllowShedsUnderOverload(t *testing.T) {
	t.Parallel()

	var shed int

	hooks := &Hooks{OnThrottled: func() { shed++ }}
	th := NewThrottler(&stubClock{now: epochBase()}, hooks, MinRequests(1))

	feed(t, th, 10, 0) // drive probability to the cap

	th.sampler = alwaysShed

	require.ErrorIs(t, th.Allow(), ErrThrottled, "call should be shed under overload")
	assert.Equal(t, 1, shed, "OnThrottled should fire once")
}

func TestAllowForwardsWhenDrawAboveProbability(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{}, MinRequests(1))

	feed(t, th, 10, 0) // probability at the 0.9 cap

	th.sampler = func() float64 { return 0.95 } // draw above the cap

	assert.NoError(t, th.Allow(), "a draw above the probability forwards")
}

func TestShedCallStillCountsAsRequest(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{}, MinRequests(1))

	feed(t, th, 10, 0)
	th.sampler = alwaysShed

	before, _ := windowSnapshot(th)
	require.ErrorIs(t, th.Allow(), ErrThrottled)
	after, _ := windowSnapshot(th)

	assert.Equal(t, before+1, after, "a shed call increments requests")
}

// ---------------------------------------------------------------------------
// Outcome classification
// ---------------------------------------------------------------------------

func TestIsRejectDefault(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{}, &Hooks{})

	assert.False(t, th.isReject(nil), "nil is always an accept")
	assert.True(t, th.isReject(errBackend), "any error is a reject by default")
}

func TestIsRejectWithClassifier(t *testing.T) {
	t.Parallel()

	notFound := errors.New("not found")
	th := NewThrottler(&stubClock{}, &Hooks{},
		ThrottleClassifier(func(err error) bool {
			return errors.Is(err, errBackend)
		}),
	)

	assert.True(t, th.isReject(errBackend), "overload error counts as reject")
	assert.False(t, th.isReject(notFound), "unclassified error is an accept")
	assert.False(t, th.isReject(nil))
}

func TestClassifierExcludesErrorFromShedding(t *testing.T) {
	t.Parallel()

	notFound := errors.New("not found")
	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{},
		MinRequests(1),
		ThrottleClassifier(func(err error) bool { return errors.Is(err, errBackend) }),
	)

	// 10 calls that all return a non-overload error: each is treated as an
	// accept, so requests == accepts and nothing is shed.
	th.sampler = neverShed
	for range 10 {
		require.NoError(t, th.Allow())
		th.Record(notFound)
	}

	assert.Zero(t, th.RejectionProbability())
}

// ---------------------------------------------------------------------------
// Sliding window: decay & recovery
// ---------------------------------------------------------------------------

func TestWindowDecaysOverTime(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	th := NewThrottler(clk, &Hooks{}, MinRequests(1), ThrottleWindow(10*time.Second))

	feed(t, th, 20, 0)
	require.Positive(t, th.RejectionProbability(), "overloaded now")

	// Advance past the whole window: every bucket ages out and the gap clears.
	clk.now = clk.now.Add(11 * time.Second)

	assert.Zero(t, th.RejectionProbability(), "window decayed → recovered")
}

func TestWindowPartialDecay(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	th := NewThrottler(clk, &Hooks{}, MinRequests(1), ThrottleWindow(10*time.Second))

	feed(t, th, 10, 0) // first second: 10 rejects
	clk.now = clk.now.Add(5 * time.Second)
	feed(t, th, 10, 10) // sixth second: 10 accepts

	// Both halves are still in the 10s window: 20 requests, 10 accepts.
	// gap = 20 - 2*10 = 0 → no shedding.
	assert.Zero(t, th.RejectionProbability())

	// Slide so only the accepting half remains: 10 requests, 10 accepts.
	clk.now = clk.now.Add(6 * time.Second)
	assert.Zero(t, th.RejectionProbability())
}

func TestWindowIgnoresFutureBucketAfterClockRewind(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	th := NewThrottler(clk, &Hooks{}, MinRequests(1), ThrottleWindow(10*time.Second))

	clk.now = clk.now.Add(20 * time.Second)
	feed(t, th, 10, 0) // bucket stamped at the later epoch

	// Rewind the clock: the recorded bucket is now in the future and must be
	// excluded from the window sum.
	clk.now = clk.now.Add(-20 * time.Second)
	requests, _ := windowSnapshot(th)

	assert.Zero(t, requests, "future-stamped bucket excluded")
}

func TestBucketForHandlesNegativeEpoch(t *testing.T) {
	t.Parallel()

	// A pre-1970 timestamp yields a negative UnixNano, hence a negative epoch
	// that is not a multiple of the bucket count, so the ring index comes out
	// negative and must be brought back into range.
	clk := &stubClock{now: time.Date(1969, time.December, 31, 23, 59, 55, 0, time.UTC)}
	th := NewThrottler(clk, &Hooks{}, MinRequests(1))

	feed(t, th, 5, 0)
	requests, _ := windowSnapshot(th)

	assert.Equal(t, int64(5), requests)
}

// ---------------------------------------------------------------------------
// Throttling predicate
// ---------------------------------------------------------------------------

func TestThrottlingPredicate(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{}, MinRequests(1))

	assert.False(t, th.Throttling(), "healthy throttler is not shedding")

	feed(t, th, 10, 0)

	assert.True(t, th.Throttling(), "overloaded throttler is shedding")
}

// ---------------------------------------------------------------------------
// Reconfigure
// ---------------------------------------------------------------------------

func TestReconfigureUpdatesParams(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{}, MinRequests(1))

	th.Reconfigure(OverloadRatio(4), MaxRejectionRate(0.5), MinRequests(7))

	assert.InEpsilon(t, 4.0, th.overloadRatio, 1e-9)
	assert.InEpsilon(t, 0.5, th.maxRejectionRate, 1e-9)
	assert.Equal(t, int64(7), th.minRequests)
}

func TestReconfigurePartialLeavesOthersUnchanged(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{},
		OverloadRatio(3), MinRequests(5))

	th.Reconfigure(MinRequests(8)) // only minRequests touched

	assert.InEpsilon(t, 3.0, th.overloadRatio, 1e-9)
	assert.Equal(t, int64(8), th.minRequests)
}

func TestReconfigureWindowResetsHistory(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	th := NewThrottler(clk, &Hooks{}, MinRequests(1), ThrottleWindow(10*time.Second))

	feed(t, th, 10, 0)
	require.Positive(t, th.RejectionProbability())

	th.Reconfigure(ThrottleWindow(20 * time.Second)) // changes bucket size

	assert.Zero(t, th.RejectionProbability(), "window change clears the history")
	assert.Equal(t, 20*time.Second, th.window)
}

func TestReconfigureSameWindowKeepsHistory(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	th := NewThrottler(clk, &Hooks{}, MinRequests(1), ThrottleWindow(10*time.Second))

	feed(t, th, 10, 0)
	require.Positive(t, th.RejectionProbability())

	th.Reconfigure(MinRequests(2)) // window unchanged → history retained

	assert.Positive(t, th.RejectionProbability())
}

func TestApplyCurrentConfigRoundTrip(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{},
		OverloadRatio(3),
		MaxRejectionRate(0.7),
		ThrottleWindow(42*time.Second),
		MinRequests(6),
	)

	// applyConfig(currentConfig()) must be the identity for every tunable, so a
	// parameter dropped from either mirror method is caught here rather than
	// silently reset on the next Reconfigure (the classifier round-trip is
	// covered by TestReconfigurePreservesClassifier).
	before := th.currentConfig()
	th.applyConfig(before)

	assert.Equal(t, before, th.currentConfig())
}

func TestReconfigurePreservesClassifier(t *testing.T) {
	t.Parallel()

	th := NewThrottler(&stubClock{now: epochBase()}, &Hooks{},
		MinRequests(1),
		ThrottleClassifier(func(err error) bool { return errors.Is(err, errBackend) }),
	)

	th.Reconfigure(OverloadRatio(3)) // numeric-only reconfigure

	notFound := errors.New("not found")
	assert.False(t, th.isReject(notFound), "classifier survives reconfigure")
}

// ---------------------------------------------------------------------------
// Concurrency
// ---------------------------------------------------------------------------

func TestThrottlerConcurrentAccess(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()
	th := NewThrottler(clk, &Hooks{}, MinRequests(1))

	const goroutines = 50

	var (
		wg       sync.WaitGroup
		accepted atomic.Int64
	)

	wg.Add(goroutines)

	for i := range goroutines {
		go func(accept bool) {
			defer wg.Done()

			if th.Allow() == nil {
				if accept {
					th.Record(nil)
					accepted.Add(1)
				} else {
					th.Record(errBackend)
				}
			}

			_ = th.RejectionProbability()
			_ = th.Throttling()
		}(i%2 == 0)
	}

	wg.Wait()

	// The clock never advances, so every Allow lands in one epoch. A lost update
	// under contention would make these counts disagree, which -race alone (a
	// memory-race detector) would not catch.
	requests, accepts := windowSnapshot(th)
	assert.Equal(t, int64(goroutines), requests, "every Allow counts one request")
	assert.Equal(t, accepted.Load(), accepts, "accepts match recorded successes")
}

// TestThrottlerConcurrentReconfigure stresses the classifier read on the
// admission path (Record → isReject) against a concurrent Reconfigure writing the
// classifier, so -race proves the snapshot-under-lock closes that data race.
func TestThrottlerConcurrentReconfigure(t *testing.T) {
	t.Parallel()

	th := NewThrottler(newPolicyClock(), &Hooks{}, MinRequests(1))

	const goroutines = 25

	var wg sync.WaitGroup

	wg.Add(2 * goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()

			if th.Allow() == nil {
				th.Record(errBackend) // reads the classifier under contention
			}
		}()

		go func() {
			defer wg.Done()

			th.Reconfigure(ThrottleClassifier(func(err error) bool {
				return errors.Is(err, errBackend)
			}))
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Policy integration
// ---------------------------------------------------------------------------

func TestPolicyAdaptiveThrottleShedsCall(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()

	var shed int

	hooks := &Hooks{OnThrottled: func() { shed++ }}
	p := NewPolicy[string]("throttle-shed",
		WithClock(clk),
		WithHooks(hooks),
		WithAdaptiveThrottle(MinRequests(1)),
	)

	// Drive the throttler to the rejection cap, then force a shed.
	feed(t, p.throttler, 10, 0)
	p.throttler.sampler = alwaysShed

	var called bool

	_, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		called = true

		return "ok", nil
	})

	require.ErrorIs(t, err, ErrThrottled)
	assert.False(t, called, "shed call must not reach the work function")
	assert.Equal(t, 1, shed)
	assert.Equal(t, int64(1), p.Metrics().Throttled)
}

func TestPolicyAdaptiveThrottleForwardsHealthy(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("throttle-ok",
		WithClock(newPolicyClock()),
		WithAdaptiveThrottle(),
	)

	val, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "ok", val)
}

func TestPolicyAdaptiveThrottleMetricsAndHealth(t *testing.T) {
	t.Parallel()

	// No OnThrottled user hook here: this also exercises the instrumented hook's
	// counter path when the caller set no callback.
	p := NewPolicy[string]("throttle-health",
		WithClock(newPolicyClock()),
		WithAdaptiveThrottle(MinRequests(1)),
	)

	feed(t, p.throttler, 10, 0)
	p.throttler.sampler = alwaysShed

	_, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})
	require.ErrorIs(t, err, ErrThrottled)

	metrics := p.Metrics()
	assert.Positive(t, metrics.ThrottleProbability)
	assert.Equal(t, int64(1), metrics.Throttled)

	status := p.HealthStatus()
	assert.Contains(t, status.Conditions, ConditionThrottling)
	assert.Equal(t, CriticalityDegraded, status.Criticality)
	assert.True(t, status.Healthy, "throttling is degraded, not critical")
}

func TestPolicyAdaptiveThrottleFromConfig(t *testing.T) {
	t.Parallel()

	ratio := 3.0
	maxRej := 0.5
	window := "20s"
	minReq := 4
	cfg := PolicyConfig{
		AdaptiveThrottle: &AdaptiveThrottleConfig{
			OverloadRatio:    &ratio,
			MaxRejectionRate: &maxRej,
			Window:           &window,
			MinRequests:      &minReq,
		},
	}

	opts, err := BuildOptions(&cfg)
	require.NoError(t, err)

	p := NewPolicy[string]("throttle-config", append(opts, WithClock(newPolicyClock()))...)
	require.NotNil(t, p.throttler)

	assert.InEpsilon(t, 3.0, p.throttler.overloadRatio, 1e-9)
	assert.InEpsilon(t, 0.5, p.throttler.maxRejectionRate, 1e-9)
	assert.Equal(t, 20*time.Second, p.throttler.window)
	assert.Equal(t, int64(4), p.throttler.minRequests)
}

func TestPolicyAdaptiveThrottleConfigBadWindow(t *testing.T) {
	t.Parallel()

	bad := "not-a-duration"
	cfg := PolicyConfig{
		AdaptiveThrottle: &AdaptiveThrottleConfig{Window: &bad},
	}

	_, err := BuildOptions(&cfg)
	require.Error(t, err)
	// The error must name the offending field and wrap the parse cause, so a
	// regression dropping the %w wrap or mislabelling the field is caught.
	assert.ErrorContains(t, err, "adaptive_throttle.window")
	assert.ErrorContains(t, err, "not-a-duration")
}

func TestPolicyReconfigureThrottle(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("throttle-reconfig",
		WithClock(newPolicyClock()),
		WithAdaptiveThrottle(OverloadRatio(2)),
	)

	ratio := 5.0
	err := p.Reconfigure(PolicyConfig{
		AdaptiveThrottle: &AdaptiveThrottleConfig{OverloadRatio: &ratio},
	})
	require.NoError(t, err)

	assert.InEpsilon(t, 5.0, p.throttler.overloadRatio, 1e-9)
}

func TestPolicyReconfigureThrottleAbsent(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("throttle-absent", WithClock(newPolicyClock()))

	ratio := 5.0
	err := p.Reconfigure(PolicyConfig{
		AdaptiveThrottle: &AdaptiveThrottleConfig{OverloadRatio: &ratio},
	})

	require.ErrorIs(t, err, ErrPatternAbsent)
}

func TestPolicyReconfigureThrottleBadWindow(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("throttle-reconfig-bad",
		WithClock(newPolicyClock()),
		WithAdaptiveThrottle(),
	)

	bad := "nope"
	err := p.Reconfigure(PolicyConfig{
		AdaptiveThrottle: &AdaptiveThrottleConfig{Window: &bad},
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "adaptive_throttle.window")
	assert.ErrorContains(t, err, "nope")
}

// ---------------------------------------------------------------------------
// Test helpers specific to the throttler
// ---------------------------------------------------------------------------

// epochBase is a fixed, post-1970 instant so window epochs are large and
// positive in tests that do not otherwise care about the wall-clock value.
func epochBase() time.Time {
	return time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
}

// windowSnapshot returns the current windowed request and accept totals.
func windowSnapshot(th *Throttler) (requests, accepts int64) {
	th.mu.Lock()
	defer th.mu.Unlock()

	return th.windowSums(th.clock.Now())
}
