package r8e

import (
	"context"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// stubClock — controllable clock for deterministic circuit breaker tests
// ---------------------------------------------------------------------------

// stubClock is NOT safe for concurrent use: its fields are read and written
// without synchronisation. Use it only in single-goroutine tests; for
// concurrent tests use the mutex-guarded policyClock instead.
type stubClock struct {
	now     time.Time
	elapsed time.Duration // returned by Since, regardless of argument
}

func (c *stubClock) Now() time.Time                { return c.now }
func (c *stubClock) Since(time.Time) time.Duration { return c.elapsed }
func (c *stubClock) NewTimer(_ time.Duration) Timer {
	return &stubTimer{}
}

// stubTimer is a minimal Timer stub for circuit breaker tests.
type stubTimer struct{}

func (*stubTimer) C() <-chan time.Time      { return make(chan time.Time) }
func (*stubTimer) Stop() bool               { return false }
func (*stubTimer) Reset(time.Duration) bool { return false }

// setElapsed sets the exact elapsed duration returned by Since.
func (c *stubClock) setElapsed(d time.Duration) {
	c.elapsed = d
}

// originClock is a deterministic clock whose Since HONOURS its argument
// (now - t), unlike stubClock (which returns a fixed elapsed regardless of the
// instant passed). It lets a test pin that a duration is measured from a
// specific stamped instant — e.g. that the ramp is measured from rampStart, not
// from a fixed elapsed or the wrong origin. Not safe for concurrent use.
type originClock struct {
	now time.Time
}

func (c *originClock) Now() time.Time                  { return c.now }
func (c *originClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }
func (c *originClock) NewTimer(time.Duration) Timer    { return &stubTimer{} }

// advance moves the clock forward by d.
func (c *originClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

// ---------------------------------------------------------------------------
// Default config values
// ---------------------------------------------------------------------------

func TestCircuitBreakerDefaultConfig(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{})

	// Verify defaults by exercising the breaker.
	// Default threshold is 5 — four failures should keep it closed.
	for range 4 {
		cb.RecordFailure()
	}
	require.NoError(t, cb.Allow())

	// The 5th failure should open it.
	cb.RecordFailure()
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
}

// ---------------------------------------------------------------------------
// Custom config values
// ---------------------------------------------------------------------------

func TestCircuitBreakerCustomConfig(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(2),
		RecoveryTimeout(10*time.Second),
		HalfOpenMaxAttempts(3),
	)

	// Two failures open the circuit (custom threshold = 2).
	cb.RecordFailure()
	cb.RecordFailure()
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)

	// Advance past custom recovery timeout (10s).
	clk.setElapsed(11 * time.Second)
	require.NoError(t, cb.Allow())
	require.Equal(t, CircuitHalfOpen, cb.State())

	// Half-open: need 3 successes to close (custom halfOpenMaxAttempts = 3).
	cb.RecordSuccess()
	require.Equal(t, CircuitHalfOpen, cb.State())
	cb.RecordSuccess()
	require.Equal(t, CircuitHalfOpen, cb.State())
	cb.RecordSuccess()
	require.Equal(t, CircuitClosed, cb.State())
}

// ---------------------------------------------------------------------------
// Closed state: allows calls
// ---------------------------------------------------------------------------

func TestClosedStateAllowsCalls(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{})

	require.NoError(t, cb.Allow())
	require.Equal(t, CircuitClosed, cb.State())
}

// ---------------------------------------------------------------------------
// Closed state: counts failures and opens at threshold
// ---------------------------------------------------------------------------

func TestClosedStateOpensAtThreshold(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{}, FailureThreshold(3))

	cb.RecordFailure()
	cb.RecordFailure()

	// Still closed after 2 failures (threshold is 3).
	require.Equal(t, CircuitClosed, cb.State())

	cb.RecordFailure()

	// Now open.
	require.Equal(t, CircuitOpen, cb.State())
}

// ---------------------------------------------------------------------------
// Open state: rejects with ErrCircuitOpen
// ---------------------------------------------------------------------------

func TestOpenStateRejectsWithErrCircuitOpen(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{}, FailureThreshold(1))

	cb.RecordFailure()

	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
}

// ---------------------------------------------------------------------------
// Open to half-open: after recovery timeout
// ---------------------------------------------------------------------------

func TestOpenToHalfOpenAfterRecoveryTimeout(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(5*time.Second),
	)

	cb.RecordFailure()

	// Still within recovery timeout.
	clk.setElapsed(4 * time.Second)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)

	// Past recovery timeout.
	clk.setElapsed(6 * time.Second)
	require.NoError(t, cb.Allow())
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// ---------------------------------------------------------------------------
// Half-open success: closes circuit
// ---------------------------------------------------------------------------

func TestHalfOpenSuccessClosesCircuit(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
	)

	cb.RecordFailure()
	clk.setElapsed(2 * time.Second)

	// Transition to half-open.
	require.NoError(t, cb.Allow())

	cb.RecordSuccess()

	require.Equal(t, CircuitClosed, cb.State())
}

// ---------------------------------------------------------------------------
// Half-open failure: reopens circuit
// ---------------------------------------------------------------------------

func TestHalfOpenFailureReopensCircuit(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
	)

	cb.RecordFailure()
	clk.setElapsed(2 * time.Second)

	// Transition to half-open.
	require.NoError(t, cb.Allow())

	cb.RecordFailure()

	require.Equal(t, CircuitOpen, cb.State())
}

// ---------------------------------------------------------------------------
// Success in closed state resets failure count
// ---------------------------------------------------------------------------

func TestSuccessInClosedStateResetsFailureCount(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{}, FailureThreshold(3))

	cb.RecordFailure()
	cb.RecordFailure()
	// 2 failures. A success should reset the count.
	cb.RecordSuccess()

	// Now record 2 more failures — should NOT open (count was reset).
	cb.RecordFailure()
	cb.RecordFailure()
	require.Equal(t, CircuitClosed, cb.State())

	// The 3rd failure after reset should open.
	cb.RecordFailure()
	require.Equal(t, CircuitOpen, cb.State())
}

// ---------------------------------------------------------------------------
// State() returns correct strings
// ---------------------------------------------------------------------------

func TestStateReturnsCorrectStrings(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
	)

	require.Equal(t, CircuitClosed, cb.State())

	cb.RecordFailure()
	require.Equal(t, CircuitOpen, cb.State())

	clk.setElapsed(2 * time.Second)
	cb.Allow() // triggers half-open
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// ---------------------------------------------------------------------------
// Hook emissions
// ---------------------------------------------------------------------------

func TestCircuitBreakerHookEmissions(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var openCount, closeCount, halfOpenCount atomic.Int64
	hooks := &Hooks{
		OnCircuitOpen:     func() { openCount.Add(1) },
		OnCircuitClose:    func() { closeCount.Add(1) },
		OnCircuitHalfOpen: func() { halfOpenCount.Add(1) },
	}

	cb := NewCircuitBreaker(clk, hooks,
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
	)

	// Trigger open.
	cb.RecordFailure()
	require.Equal(t, int64(1), openCount.Load())

	// Trigger half-open.
	clk.setElapsed(2 * time.Second)
	cb.Allow()
	require.Equal(t, int64(1), halfOpenCount.Load())

	// Trigger close.
	cb.RecordSuccess()
	require.Equal(t, int64(1), closeCount.Load())
}

func TestCircuitBreakerHookOnReopenFromHalfOpen(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var openCount atomic.Int64
	hooks := &Hooks{
		OnCircuitOpen: func() { openCount.Add(1) },
	}

	cb := NewCircuitBreaker(clk, hooks,
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
	)

	// Open.
	cb.RecordFailure()
	require.Equal(t, int64(1), openCount.Load())

	// Half-open.
	clk.setElapsed(2 * time.Second)
	cb.Allow()

	// Failure in half-open should re-open and fire hook again.
	cb.RecordFailure()
	require.Equal(t, int64(2), openCount.Load())
}

// ---------------------------------------------------------------------------
// Concurrent access: 100 goroutines doing Allow/RecordSuccess/RecordFailure
// ---------------------------------------------------------------------------

func TestCircuitBreakerConcurrentAccess(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(10),
		RecoveryTimeout(1*time.Second),
	)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			_ = cb.Allow()
			cb.RecordSuccess()
			cb.RecordFailure()
			_ = cb.State()
		}()
	}

	wg.Wait()

	// Just verify it didn't panic or race — the race detector will catch
	// issues.
	state := cb.State()
	assert.Contains(t, []CircuitState{CircuitClosed, CircuitOpen, CircuitHalfOpen}, state)
}

// TestCircuitBreakerStateFailsSafe forces an unrecognised internal state and
// asserts State() reports open rather than healthy — so a future state added
// without updating the State() switch can never look ready.
func TestCircuitBreakerStateFailsSafe(t *testing.T) {
	t.Parallel()

	cb := NewCircuitBreaker(&stubClock{now: time.Now()}, &Hooks{})
	cb.state = 99 // not stateClosed/stateOpen/stateHalfOpen

	assert.Equal(t, CircuitOpen, cb.State())
}

// TestCircuitBreakerRecoveryBoundaryInclusive pins the recovery-window
// comparison: at exactly RecoveryTimeout the breaker must still reject (the
// check is `<=`, inclusive). A `<` mutation would admit a probe one tick early.
func TestCircuitBreakerRecoveryBoundaryInclusive(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(time.Second),
	)

	cb.RecordFailure() // threshold 1 → open
	require.Equal(t, CircuitOpen, cb.State())

	clk.setElapsed(time.Second) // exactly at the boundary

	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
}

// ---------------------------------------------------------------------------
// Slow-call-rate tripping (C3)
// ---------------------------------------------------------------------------

// TestSlowCallRateOpensOnSlowSuccesses verifies that slow — but successful —
// calls open the breaker once their fraction over the window reaches the
// threshold, independently of the failure trip, and that both the open and the
// slow-call-reason hooks fire.
func TestSlowCallRateOpensOnSlowSuccesses(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var slowTrips, opens atomic.Int64

	cb := NewCircuitBreaker(clk, &Hooks{
		OnCircuitOpen:          func() { opens.Add(1) },
		OnSlowCallRateExceeded: func() { slowTrips.Add(1) },
	},
		FailureThreshold(100), // keep the failure trip out of the way
		SlowCallRate(100*time.Millisecond, 0.5),
		SlowCallWindow(4),
		SlowCallMinCalls(4),
	)

	cb.Record(50*time.Millisecond, nil)
	cb.Record(50*time.Millisecond, nil)
	require.Equal(t, CircuitClosed, cb.State())

	cb.Record(200*time.Millisecond, nil)
	require.Equal(t, CircuitClosed, cb.State()) // filled 3 < minCalls 4

	cb.Record(200*time.Millisecond, nil) // filled 4, 2/4 = 0.5 >= 0.5 -> open
	require.Equal(t, CircuitOpen, cb.State())
	require.Equal(t, int64(1), slowTrips.Load())
	require.Equal(t, int64(1), opens.Load())
}

// TestSlowCallFailedAndSlowTripsBySlowRate covers the failed-and-slow path: when
// failing calls are also slow but stay below the failure threshold, the breaker
// opens on the slow-call rate and attributes the open to the slow-call cause.
func TestSlowCallFailedAndSlowTripsBySlowRate(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var slowTrips, opens atomic.Int64

	cb := NewCircuitBreaker(clk, &Hooks{
		OnCircuitOpen:          func() { opens.Add(1) },
		OnSlowCallRateExceeded: func() { slowTrips.Add(1) },
	},
		FailureThreshold(100), // keep the failure trip out of the way
		SlowCallRate(100*time.Millisecond, 0.5),
		SlowCallWindow(4),
		SlowCallMinCalls(4),
	)

	boom := errors.New("boom")
	for range 4 {
		cb.Record(200*time.Millisecond, boom) // failing AND slow
	}

	require.Equal(t, CircuitOpen, cb.State())
	require.Equal(t, int64(1), slowTrips.Load())
	require.Equal(t, int64(1), opens.Load())
}

// TestSlowCallFailurePrecedenceOverSlowRate pins the documented precedence: when
// a single call both reaches the failure threshold and trips the slow-call rate,
// the failure trip wins and the open is attributed to the failure cause.
func TestSlowCallFailurePrecedenceOverSlowRate(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var slowTrips, opens atomic.Int64

	cb := NewCircuitBreaker(clk, &Hooks{
		OnCircuitOpen:          func() { opens.Add(1) },
		OnSlowCallRateExceeded: func() { slowTrips.Add(1) },
	},
		FailureThreshold(4),
		SlowCallRate(100*time.Millisecond, 0.5),
		SlowCallWindow(4),
		SlowCallMinCalls(4),
	)

	boom := errors.New("boom")
	for range 4 {
		cb.Record(200*time.Millisecond, boom) // failing AND slow
	}

	// On the 4th call both conditions hold; the failure trip takes precedence.
	require.Equal(t, CircuitOpen, cb.State())
	assert.Equal(t, int64(1), opens.Load())
	assert.Zero(t, slowTrips.Load())
}

// TestCircuitBreakerFailureWhileOpenAdvancesBaseline pins the historical
// contract: a failure recorded while the breaker is already open drives no
// transition but pushes the recovery baseline (lastFailure) forward.
func TestCircuitBreakerFailureWhileOpenAdvancesBaseline(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{}, FailureThreshold(1))

	cb.RecordFailure() // opens (threshold 1); lastFailure = now
	require.Equal(t, CircuitOpen, cb.State())

	first := cb.lastFailure
	clk.now = clk.now.Add(time.Hour)
	cb.RecordFailure() // already open: no transition, baseline advances

	assert.True(t, cb.lastFailure.After(first))
	assert.Equal(t, CircuitOpen, cb.State())
}

// TestSlowCallRateBelowThresholdStaysClosed pins that a slow fraction strictly
// below the threshold does not trip.
func TestSlowCallRateBelowThresholdStaysClosed(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(100),
		SlowCallRate(100*time.Millisecond, 0.75),
		SlowCallWindow(4),
		SlowCallMinCalls(4),
	)

	cb.Record(200*time.Millisecond, nil)
	cb.Record(200*time.Millisecond, nil)
	cb.Record(50*time.Millisecond, nil)
	cb.Record(50*time.Millisecond, nil) // 2/4 = 0.5 < 0.75

	require.Equal(t, CircuitClosed, cb.State())
}

// TestSlowCallMinCallsGate verifies the minimum-calls gate: even a 100% slow
// fraction cannot trip the breaker until enough calls have been observed.
func TestSlowCallMinCallsGate(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(100),
		SlowCallRate(100*time.Millisecond, 0.5),
		SlowCallWindow(100),
		SlowCallMinCalls(10),
	)

	for range 5 {
		cb.Record(200*time.Millisecond, nil) // all slow, but < minCalls
	}

	require.Equal(t, CircuitClosed, cb.State())
	assert.InDelta(t, 1.0, cb.SlowCallFraction(), 1e-9)

	for range 5 {
		cb.Record(200*time.Millisecond, nil) // filled reaches 10 -> trips
	}

	require.Equal(t, CircuitOpen, cb.State())
}

// TestSlowCallWindowSlidesAndTrips proves the count-based window evicts the
// oldest verdict: a fast call falling out of the window raises the slow
// fraction to the threshold and opens the breaker.
func TestSlowCallWindowSlidesAndTrips(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(100),
		SlowCallRate(100*time.Millisecond, 1.0), // only an all-slow window trips
		SlowCallWindow(3),
		SlowCallMinCalls(3),
	)

	cb.Record(50*time.Millisecond, nil)  // [fast]
	cb.Record(200*time.Millisecond, nil) // [fast, slow]
	cb.Record(200*time.Millisecond, nil) // [fast, slow, slow] -> 2/3 < 1.0
	require.Equal(t, CircuitClosed, cb.State())

	cb.Record(200*time.Millisecond, nil) // evicts fast -> [slow, slow, slow] = 1.0
	require.Equal(t, CircuitOpen, cb.State())
}

// TestSlowCallHalfOpenSlowProbeReopens locks the chosen half-open semantics: a
// successful but slow probe is treated as unhealthy and reopens the breaker via
// the slow-call rule (surfacing OnSlowCallRateExceeded).
func TestSlowCallHalfOpenSlowProbeReopens(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var slowTrips, opens atomic.Int64

	cb := NewCircuitBreaker(clk, &Hooks{
		OnCircuitOpen:          func() { opens.Add(1) },
		OnSlowCallRateExceeded: func() { slowTrips.Add(1) },
	},
		FailureThreshold(1),
		RecoveryTimeout(time.Second),
		HalfOpenMaxAttempts(1),
		SlowCallRate(100*time.Millisecond, 0.5),
		SlowCallWindow(100),
		SlowCallMinCalls(100), // keep the closed-state slow trip out of the way
	)

	cb.RecordFailure() // threshold 1 -> open
	require.Equal(t, int64(1), opens.Load())

	clk.setElapsed(2 * time.Second)
	require.NoError(t, cb.Allow()) // -> half-open

	cb.Record(200*time.Millisecond, nil) // slow but successful probe -> reopen
	require.Equal(t, CircuitOpen, cb.State())
	require.Equal(t, int64(1), slowTrips.Load())
	require.Equal(t, int64(2), opens.Load())
}

// TestSlowCallHalfOpenFastProbeCloses verifies a fast successful probe still
// closes the breaker — slow-call detection must not interfere with recovery.
func TestSlowCallHalfOpenFastProbeCloses(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var slowTrips atomic.Int64

	cb := NewCircuitBreaker(clk, &Hooks{
		OnSlowCallRateExceeded: func() { slowTrips.Add(1) },
	},
		FailureThreshold(1),
		RecoveryTimeout(time.Second),
		HalfOpenMaxAttempts(1),
		SlowCallRate(100*time.Millisecond, 0.5),
	)

	cb.RecordFailure()
	clk.setElapsed(2 * time.Second)
	require.NoError(t, cb.Allow())

	cb.Record(50*time.Millisecond, nil) // fast successful probe -> close
	require.Equal(t, CircuitClosed, cb.State())
	require.Zero(t, slowTrips.Load())
}

// TestSlowCallDisabledByDefault confirms that without SlowCallRate even very
// slow calls never trip on latency and the gauge stays zero.
func TestSlowCallDisabledByDefault(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{}, FailureThreshold(100))

	for range 50 {
		cb.Record(10*time.Second, nil)
	}

	require.Equal(t, CircuitClosed, cb.State())
	assert.Zero(t, cb.SlowCallFraction())
}

// TestSlowCallFractionGauge checks the SlowCallFraction gauge tracks the window.
func TestSlowCallFractionGauge(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(100),
		SlowCallRate(100*time.Millisecond, 0.99),
		SlowCallWindow(10),
		SlowCallMinCalls(10),
	)

	assert.Zero(t, cb.SlowCallFraction())

	cb.Record(200*time.Millisecond, nil)
	cb.Record(200*time.Millisecond, nil)
	cb.Record(50*time.Millisecond, nil)
	cb.Record(50*time.Millisecond, nil)

	assert.InDelta(t, 0.5, cb.SlowCallFraction(), 1e-9)
}

// TestSlowCallDurationBoundaryIsFast pins the slow/fast classifier boundary: a
// call whose latency is exactly the threshold is fast (the comparison is strict
// `>`). A `>=` mutation would classify it slow and trip one notch early.
func TestSlowCallDurationBoundaryIsFast(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(100),
		SlowCallRate(100*time.Millisecond, 0.5),
		SlowCallWindow(4),
		SlowCallMinCalls(4),
	)

	for range 4 {
		cb.Record(100*time.Millisecond, nil) // exactly at the threshold -> fast
	}

	require.Equal(t, CircuitClosed, cb.State())
	assert.Zero(t, cb.SlowCallFraction())
}

// TestSlowCallRateClampsAndGuards exercises the option clamps: rate is bounded
// to [0,1], a non-positive rate or duration disables detection, and sub-1
// window/min-calls values are ignored.
func TestSlowCallRateClampsAndGuards(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	cb := NewCircuitBreaker(clk, &Hooks{}, SlowCallRate(time.Millisecond, 2.0))
	assert.InDelta(t, 1.0, cb.cfg.slowCallRateThreshold, 1e-9)
	assert.True(t, cb.slowCallEnabled())

	cbZeroRate := NewCircuitBreaker(clk, &Hooks{}, SlowCallRate(time.Millisecond, 0))
	assert.False(t, cbZeroRate.slowCallEnabled())

	cbNoDur := NewCircuitBreaker(clk, &Hooks{}, SlowCallRate(0, 0.5))
	assert.False(t, cbNoDur.slowCallEnabled())

	cbNeg := NewCircuitBreaker(clk, &Hooks{}, SlowCallRate(time.Millisecond, -0.5))
	assert.InDelta(t, 0.0, cbNeg.cfg.slowCallRateThreshold, 1e-9)
	assert.False(t, cbNeg.slowCallEnabled())

	cbWin := NewCircuitBreaker(clk, &Hooks{}, SlowCallWindow(0), SlowCallMinCalls(-3))
	assert.Equal(t, 100, cbWin.cfg.slowCallWindow)
	assert.Equal(t, 10, cbWin.cfg.slowCallMinCalls)
}

// TestSlowCallWindowEvictsSlowEntry covers the eviction arithmetic when the
// oldest verdict leaving the window was itself slow (slowCount must decrement).
func TestSlowCallWindowEvictsSlowEntry(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(100),
		SlowCallRate(100*time.Millisecond, 1.0),
		SlowCallWindow(2),
		SlowCallMinCalls(100), // observe the ring without tripping
	)

	cb.Record(200*time.Millisecond, nil) // [slow]
	cb.Record(200*time.Millisecond, nil) // [slow, slow]
	require.Equal(t, 2, cb.slowWin.slow)

	cb.Record(50*time.Millisecond, nil) // evicts a slow -> [fast, slow]
	assert.Equal(t, 1, cb.slowWin.slow)
	assert.Equal(t, 2, cb.slowWin.filled)
}

// TestSlowCallDefensiveFloors forces degenerate window/min-calls config (which
// the options would normally reject) to exercise the defensive floors: a zero
// window must not divide by zero, and a zero min-calls must still gate at one.
func TestSlowCallDefensiveFloors(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{}, SlowCallRate(100*time.Millisecond, 0.5))

	cb.cfg.slowCallWindow = 0
	cb.cfg.slowCallMinCalls = 0

	cb.Record(200*time.Millisecond, nil) // slow; ring floored to size 1 -> trips
	assert.Equal(t, CircuitOpen, cb.State())
	assert.Len(t, cb.slowWin.ring, 1)
}

// TestSlowCallReconfigureResetsWindow verifies that changing the window size at
// runtime reallocates the ring and resets the accumulated history.
func TestSlowCallReconfigureResetsWindow(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(100),
		SlowCallRate(100*time.Millisecond, 1.0),
		SlowCallWindow(4),
		SlowCallMinCalls(4),
	)

	cb.Record(200*time.Millisecond, nil)
	cb.Record(200*time.Millisecond, nil)
	require.Equal(t, 2, cb.slowWin.filled)

	cb.Reconfigure(SlowCallWindow(8))
	cb.Record(200*time.Millisecond, nil)

	assert.Equal(t, 1, cb.slowWin.filled)
	assert.Len(t, cb.slowWin.ring, 8)
}

// TestPolicySlowCallRateMetricsAndMiddleware exercises the policy middleware:
// it measures latency with the injected clock, drives the breaker open via the
// slow-call rate, and surfaces the dedicated counter, gauge, and hook.
func TestPolicySlowCallRateMetricsAndMiddleware(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	clk.setElapsed(200 * time.Millisecond) // every measured call looks slow

	var slowTrips atomic.Int64

	p := NewPolicy[string]("slow-call",
		WithClock(clk),
		WithHooks(&Hooks{OnSlowCallRateExceeded: func() { slowTrips.Add(1) }}),
		WithCircuitBreaker(
			FailureThreshold(1000),
			SlowCallRate(100*time.Millisecond, 0.5),
			SlowCallWindow(10),
			SlowCallMinCalls(10),
		),
	)

	ctx := context.Background()
	ok := func(_ context.Context) (string, error) { return "ok", nil }

	for range 10 {
		_, _ = p.Do(ctx, ok)
	}

	metrics := p.Metrics()
	assert.Equal(t, "open", metrics.CircuitState)
	assert.GreaterOrEqual(t, metrics.SlowCallRateExceeded, int64(1))
	assert.GreaterOrEqual(t, metrics.CircuitOpens, int64(1))
	assert.InDelta(t, 1.0, metrics.SlowCallRate, 1e-9)
	assert.Equal(t, int64(1), slowTrips.Load())

	_, err := p.Do(ctx, ok) // breaker now open -> fast reject
	require.ErrorIs(t, err, ErrCircuitOpen)
}

// ---------------------------------------------------------------------------
// Adaptive recovery backoff (C3)
// ---------------------------------------------------------------------------

// TestRecoveryBackoffDisabledByDefault verifies that without
// RecoveryBackoffMultiplier the breaker always uses the base recoveryTimeout,
// even after multiple failed probes.
func TestRecoveryBackoffDisabledByDefault(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(10*time.Second),
	)

	for range 3 {
		cb.RecordFailure()                 // open (or already open)
		clk.setElapsed(10*time.Second - 1) // just below base timeout
		require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)

		clk.setElapsed(10*time.Second + 1) // just above base timeout
		require.NoError(t, cb.Allow())     // half-open
		cb.RecordFailure()                 // re-open
	}
}

// TestRecoveryBackoffNotAppliedOnFirstTrip verifies that the first trip from
// closed always uses the base recoveryTimeout (recoveryAttempt starts at 0).
func TestRecoveryBackoffNotAppliedOnFirstTrip(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(10*time.Second),
		RecoveryBackoffMultiplier(100.0), // would be enormous if applied
	)

	cb.RecordFailure() // first trip: recoveryAttempt=0
	require.Equal(t, CircuitOpen, cb.State())

	clk.setElapsed(10*time.Second - 1)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen) // base timeout not yet elapsed

	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow()) // base timeout elapsed → half-open
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// TestRecoveryBackoffGrowsExponentially verifies that each failed half-open
// probe doubles the recovery wait (with factor=2).
func TestRecoveryBackoffGrowsExponentially(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(10*time.Second),
		HalfOpenMaxAttempts(1),
		RecoveryBackoffMultiplier(2.0),
	)

	// 1st trip (from closed): base timeout = 10s.
	cb.RecordFailure()
	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow()) // half-open
	cb.RecordFailure()             // 1st failed probe: recoveryAttempt=1

	// 2nd open: timeout = 10s × 2^1 = 20s.
	clk.setElapsed(15 * time.Second)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen) // 15s < 20s

	clk.setElapsed(20*time.Second + 1)
	require.NoError(t, cb.Allow()) // half-open
	cb.RecordFailure()             // 2nd failed probe: recoveryAttempt=2

	// 3rd open: timeout = 10s × 2^2 = 40s.
	clk.setElapsed(30 * time.Second)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen) // 30s < 40s

	clk.setElapsed(40*time.Second + 1)
	require.NoError(t, cb.Allow()) // half-open
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// TestRecoveryBackoffCappedByMax verifies that RecoveryMaxBackoff caps the
// computed exponential value.
func TestRecoveryBackoffCappedByMax(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(10*time.Second),
		RecoveryBackoffMultiplier(10.0),    // uncapped: 100s after first failed probe
		RecoveryMaxBackoff(30*time.Second), // should clamp at 30s
	)

	cb.RecordFailure()
	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow()) // half-open
	cb.RecordFailure()             // 1st failed probe: would be 100s, capped at 30s

	clk.setElapsed(25 * time.Second)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen) // 25s < cap 30s

	clk.setElapsed(30*time.Second + 1)
	require.NoError(t, cb.Allow()) // 30s+ past cap → half-open
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// TestRecoveryBackoffResetsOnClose verifies that a successful close resets the
// backoff counter so the next trip starts from the base recoveryTimeout.
func TestRecoveryBackoffResetsOnClose(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(10*time.Second),
		RecoveryBackoffMultiplier(2.0),
	)

	// Trip and accumulate one failed probe: next timeout would be 20s.
	cb.RecordFailure()
	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow())
	cb.RecordFailure() // recoveryAttempt=1

	// Now close via a successful probe.
	clk.setElapsed(20*time.Second + 1)
	require.NoError(t, cb.Allow())
	cb.RecordSuccess() // close: recoveryAttempt should reset to 0
	require.Equal(t, CircuitClosed, cb.State())

	// Next trip from closed must use the base timeout (10s), not 20s.
	cb.RecordFailure()
	require.Equal(t, CircuitOpen, cb.State())

	clk.setElapsed(9 * time.Second)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen) // 9s < 10s: still waiting

	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow()) // base timeout elapsed → half-open
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// TestRecoveryBackoffSlowProbeIncrementsAttempt verifies that a slow (not failed)
// probe re-opening the breaker also increments the backoff counter.
func TestRecoveryBackoffSlowProbeIncrementsAttempt(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(100),
		RecoveryTimeout(10*time.Second),
		RecoveryBackoffMultiplier(2.0),
		SlowCallRate(100*time.Millisecond, 0.5),
		SlowCallWindow(4),
		SlowCallMinCalls(4),
	)

	// Trip via slow-call rate to put breaker open.
	for range 4 {
		cb.Record(200*time.Millisecond, nil)
	}
	require.Equal(t, CircuitOpen, cb.State())

	// Probe: a slow (but not failed) result re-opens with incremented backoff.
	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow())       // half-open
	cb.Record(200*time.Millisecond, nil) // slow probe → re-opens: recoveryAttempt=1

	require.Equal(t, CircuitOpen, cb.State())

	// Verify next timeout is 20s (base 10s × 2^1).
	clk.setElapsed(15 * time.Second)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)

	clk.setElapsed(20*time.Second + 1)
	require.NoError(t, cb.Allow())
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// TestRecoveryBackoffConfigRoundTrip verifies that the config path (JSON/YAML
// struct) correctly maps RecoveryBackoffMultiplier and RecoveryMaxBackoff to the
// circuit breaker options.
func TestRecoveryBackoffConfigRoundTrip(t *testing.T) {
	t.Parallel()

	multiplier := 2.0
	maxBackoff := "20s"
	opts, err := cbOptionsFromConfig(&CircuitBreakerConfig{
		RecoveryBackoffMultiplier: &multiplier,
		RecoveryMaxBackoff:        &maxBackoff,
	})
	require.NoError(t, err)

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		append(opts, FailureThreshold(1), RecoveryTimeout(10*time.Second))...,
	)

	cb.RecordFailure()
	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow())
	cb.RecordFailure() // recoveryAttempt=1: would be 20s, capped at 20s (equal)

	clk.setElapsed(19 * time.Second)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)

	clk.setElapsed(20*time.Second + 1)
	require.NoError(t, cb.Allow())
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// TestRecoveryMaxBackoffInvalidDuration verifies that an unparseable duration
// string for RecoveryMaxBackoff returns an error from cbOptionsFromConfig.
func TestRecoveryMaxBackoffInvalidDuration(t *testing.T) {
	t.Parallel()

	invalid := "not-a-duration"
	_, err := cbOptionsFromConfig(&CircuitBreakerConfig{
		RecoveryMaxBackoff: &invalid,
	})
	require.ErrorContains(t, err, "circuit_breaker.recovery_max_backoff")
}

// TestRampRecoveryConfigRoundTrip verifies the config path maps RampRecovery,
// RampAggression and RampInitialFraction to the circuit breaker options.
func TestRampRecoveryConfigRoundTrip(t *testing.T) {
	t.Parallel()

	window := "100s"
	aggression := 2.0
	initial := 0.0
	opts, err := cbOptionsFromConfig(&CircuitBreakerConfig{
		RampRecovery:        &window,
		RampAggression:      &aggression,
		RampInitialFraction: &initial,
	})
	require.NoError(t, err)

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		append(opts, FailureThreshold(1), RecoveryTimeout(1*time.Second), HalfOpenMaxAttempts(1))...,
	)

	driveToRamp(t, clk, cb)
	clk.setElapsed(25 * time.Second) // tf=0.25; aggression 2 (sqrt) → 0.5

	assert.InDelta(t, 0.5, cb.RampRecoveryFraction(), 1e-9)
}

// TestRampRecoveryInvalidDuration verifies an unparseable RampRecovery duration
// string returns an error from cbOptionsFromConfig.
func TestRampRecoveryInvalidDuration(t *testing.T) {
	t.Parallel()

	invalid := "not-a-duration"
	_, err := cbOptionsFromConfig(&CircuitBreakerConfig{
		RampRecovery: &invalid,
	})
	require.ErrorContains(t, err, "circuit_breaker.ramp_recovery")
}

// TestRecoveryBackoffReconfigureResetsAttempt verifies that enabling backoff via
// Reconfigure (disabled → enabled) resets recoveryAttempt so the first probe
// after reconfiguration uses the base timeout, not a stale accumulated count.
func TestRecoveryBackoffReconfigureResetsAttempt(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(10*time.Second),
	)

	// Trip and accumulate a failed probe without backoff.
	cb.RecordFailure()
	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow())
	cb.RecordFailure() // would be recoveryAttempt=0 (backoff disabled)

	// Now enable backoff. recoveryAttempt must reset to 0.
	cb.Reconfigure(RecoveryBackoffMultiplier(100.0))

	// With a stale recoveryAttempt > 0 the timeout would be enormous.
	// A clean reset means the base 10s should apply.
	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow()) // base timeout — not 1000s
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// TestRecoveryBackoffMultiplierOnlyConfig verifies that cbOptionsFromConfig
// handles a config with RecoveryBackoffMultiplier set but no RecoveryMaxBackoff
// (the common case — uncapped growth).
func TestRecoveryBackoffMultiplierOnlyConfig(t *testing.T) {
	t.Parallel()

	multiplier := 3.0
	opts, err := cbOptionsFromConfig(&CircuitBreakerConfig{
		RecoveryBackoffMultiplier: &multiplier,
	})
	require.NoError(t, err)

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		append(opts, FailureThreshold(1), RecoveryTimeout(10*time.Second))...,
	)

	cb.RecordFailure()
	clk.setElapsed(10*time.Second + 1)
	require.NoError(t, cb.Allow())
	cb.RecordFailure() // recoveryAttempt=1: timeout = 10s × 3^1 = 30s

	clk.setElapsed(20 * time.Second)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen) // 20s < 30s

	clk.setElapsed(30*time.Second + 1)
	require.NoError(t, cb.Allow())
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// ---------------------------------------------------------------------------
// Ramp recovery (slow-start)
// ---------------------------------------------------------------------------

// driveToRamp trips the breaker, waits out the recovery timeout into half-open,
// then succeeds the single probe to enter the ramping state. It leaves the
// clock's elapsed at 0 so the caller can position the ramp via clk.setElapsed.
// The breaker must be configured FailureThreshold(1)/HalfOpenMaxAttempts(1) with
// ramp recovery enabled (the recovery timeout may be anything below one hour).
func driveToRamp(t *testing.T, clk *stubClock, cb *CircuitBreaker) {
	t.Helper()

	cb.RecordFailure()             // trip from closed
	clk.setElapsed(1 * time.Hour)  // exceed any configured recovery timeout
	require.NoError(t, cb.Allow()) // → half-open, first probe admitted
	cb.RecordSuccess()             // probe succeeds → ramping
	require.Equal(t, CircuitRamping, cb.State())

	clk.setElapsed(0) // reset to the start of the ramp window
}

// TestRampFraction pins the Envoy slow-start admission curve in isolation.
func TestRampFraction(t *testing.T) {
	t.Parallel()

	const window = 10 * time.Second

	tests := []struct {
		name       string
		elapsed    time.Duration
		aggression float64
		initial    float64
		want       float64
	}{
		{"start floored at initial", 0, 1.0, 0.1, 0.1},
		{"linear midpoint", 5 * time.Second, 1.0, 0.1, 0.5},
		{"linear three-quarters", 75 * time.Second / 10, 1.0, 0.1, 0.75},
		{"at window admits all", window, 1.0, 0.1, 1.0},
		{"past window admits all", 2 * window, 1.0, 0.1, 1.0},
		{"negative elapsed floored to start", -5 * time.Second, 1.0, 0.2, 0.2},
		{"aggression 2 ramps faster (sqrt)", 25 * time.Second / 10, 2.0, 0.0, 0.5},
		{"aggression below initial uses floor", 1 * time.Second, 2.0, 0.5, 0.5},
		{"non-positive aggression falls back to linear", 5 * time.Second, 0, 0.1, 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := rampFraction(tt.elapsed, window, tt.aggression, tt.initial)
			assert.InDelta(t, tt.want, got, 1e-9)
		})
	}
}

// TestRampEntersRampingAfterRecovery verifies a recovered half-open probe enters
// the ramping state (instead of closing) and fires OnCircuitRamping once.
func TestRampEntersRampingAfterRecovery(t *testing.T) {
	t.Parallel()

	var ramps, closes atomic.Int64

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{
		OnCircuitRamping: func() { ramps.Add(1) },
		OnCircuitClose:   func() { closes.Add(1) },
	},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(10*time.Second),
	)

	driveToRamp(t, clk, cb)

	assert.Equal(t, int64(1), ramps.Load(), "OnCircuitRamping fires once on ramp entry")
	assert.Equal(t, int64(0), closes.Load(), "breaker has not closed yet")
}

// TestRampAdmitsByFraction verifies the probabilistic gate: a draw below the
// ramp fraction is admitted, a draw at or above it is shed with ErrCircuitRamping
// (distinct from ErrCircuitOpen).
func TestRampAdmitsByFraction(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(10*time.Second), // linear, initial 0.1
	)

	driveToRamp(t, clk, cb)
	clk.setElapsed(5 * time.Second) // fraction = 0.5

	cb.sampler = func() float64 { return 0.4 }
	require.NoError(t, cb.Allow(), "draw below fraction is admitted")
	require.Equal(t, CircuitRamping, cb.State(), "admitting does not change state")

	cb.sampler = func() float64 { return 0.5 }
	err := cb.Allow()
	require.ErrorIs(t, err, ErrCircuitRamping, "draw at the fraction sheds (>=)")
	require.NotErrorIs(t, err, ErrCircuitOpen, "ramp shed is distinct from a full open")

	cb.sampler = func() float64 { return 0.6 }
	require.ErrorIs(t, cb.Allow(), ErrCircuitRamping, "draw above fraction sheds")
}

// TestRampInitialFractionFloorsAdmission verifies that at the very start of the
// ramp the admitted fraction is floored at RampInitialFraction.
func TestRampInitialFractionFloorsAdmission(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(10*time.Second),
		RampInitialFraction(0.3),
	)

	driveToRamp(t, clk, cb) // elapsed 0 → fraction = initial 0.3
	assert.InDelta(t, 0.3, cb.RampRecoveryFraction(), 1e-9)

	cb.sampler = func() float64 { return 0.29 }
	require.NoError(t, cb.Allow(), "below the floor is admitted")

	cb.sampler = func() float64 { return 0.3 }
	require.ErrorIs(t, cb.Allow(), ErrCircuitRamping, "at the floor sheds")
}

// TestRampInitialFractionClamped verifies RampInitialFraction is clamped to
// [0, 1] like other fractions.
func TestRampInitialFractionClamped(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(10*time.Second),
		RampInitialFraction(5.0), // clamped to 1.0 → admit everything immediately
	)

	driveToRamp(t, clk, cb)
	assert.InDelta(t, 1.0, cb.RampRecoveryFraction(), 1e-9)

	cb.sampler = func() float64 { return 0.999 }
	require.NoError(t, cb.Allow())
}

// TestRampAggressionAdmitsMoreEarly verifies a higher aggression admits a larger
// fraction early in the window than the linear default.
func TestRampAggressionAdmitsMoreEarly(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(100*time.Second),
		RampAggression(2.0), // sqrt curve
		RampInitialFraction(0),
	)

	driveToRamp(t, clk, cb)
	clk.setElapsed(25 * time.Second) // tf=0.25; sqrt → 0.5 (linear would be 0.25)
	assert.InDelta(t, 0.5, cb.RampRecoveryFraction(), 1e-9)
}

// TestRampNonPositiveAggressionIgnored verifies RampAggression(≤0) keeps the
// default linear curve.
func TestRampNonPositiveAggressionIgnored(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(10*time.Second),
		RampAggression(-1.0), // ignored → linear default
		RampInitialFraction(0),
	)

	driveToRamp(t, clk, cb)
	clk.setElapsed(5 * time.Second)
	assert.InDelta(t, 0.5, cb.RampRecoveryFraction(), 1e-9, "stays linear")
}

// TestRampCompletesAndCloses verifies that once the ramp window elapses the next
// Allow closes the breaker and fires OnCircuitClose.
func TestRampCompletesAndCloses(t *testing.T) {
	t.Parallel()

	var closes atomic.Int64

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{
		OnCircuitClose: func() { closes.Add(1) },
	},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(10*time.Second),
	)

	driveToRamp(t, clk, cb)

	clk.setElapsed(10 * time.Second) // window fully elapsed (inclusive)
	require.NoError(t, cb.Allow())
	assert.Equal(t, CircuitClosed, cb.State())
	assert.Equal(t, int64(1), closes.Load())
}

// TestRampFailureReopensAndCarriesBackoff verifies a failure during the ramp
// reopens the breaker, and — because reaching the ramp is only partial recovery
// — bumps the adaptive-recovery backoff (recoveryAttempt is not reset on ramp
// entry).
func TestRampFailureReopensAndCarriesBackoff(t *testing.T) {
	t.Parallel()

	var opens atomic.Int64

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{
		OnCircuitOpen: func() { opens.Add(1) },
	},
		FailureThreshold(1),
		RecoveryTimeout(10*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(60*time.Second),
		RecoveryBackoffMultiplier(2.0),
	)

	driveToRamp(t, clk, cb)
	clk.setElapsed(5 * time.Second)

	cb.RecordFailure() // failure during ramp → reopen, recoveryAttempt 0→1
	require.Equal(t, CircuitOpen, cb.State())
	require.Equal(t, int64(2), opens.Load(), "one open on the initial trip, one on the ramp failure")

	// Backoff carried: next recovery wait = 10s × 2^1 = 20s, not the base 10s.
	clk.setElapsed(15 * time.Second)
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen, "15s < 20s backed-off wait")

	clk.setElapsed(20*time.Second + 1)
	require.NoError(t, cb.Allow())
	require.Equal(t, CircuitHalfOpen, cb.State())
}

// TestRampSlowCallReopens verifies a slow (but successful) call during the ramp
// reopens the breaker via the slow-call cause, when slow-call detection is on.
func TestRampSlowCallReopens(t *testing.T) {
	t.Parallel()

	var slowTrips, opens atomic.Int64

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{
		OnCircuitOpen:          func() { opens.Add(1) },
		OnSlowCallRateExceeded: func() { slowTrips.Add(1) },
	},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(60*time.Second),
		SlowCallRate(100*time.Millisecond, 0.5),
	)

	driveToRamp(t, clk, cb)
	clk.setElapsed(5 * time.Second)

	cb.Record(200*time.Millisecond, nil) // slow but successful → reopen
	assert.Equal(t, CircuitOpen, cb.State())
	assert.Equal(t, int64(2), opens.Load())
	assert.Equal(t, int64(1), slowTrips.Load(), "reopen surfaced as a slow-call cause")
}

// TestRampSuccessKeepsRamping verifies a fast success during the ramp neither
// closes nor reopens — progression is time-driven, handled lazily in Allow.
func TestRampSuccessKeepsRamping(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(10*time.Second),
	)

	driveToRamp(t, clk, cb)
	clk.setElapsed(5 * time.Second)

	cb.RecordSuccess()
	assert.Equal(t, CircuitRamping, cb.State(), "success mid-ramp keeps ramping")
}

// TestRampDisabledClosesDirectly pins the backward-compatible default: with no
// RampRecovery, a recovered probe closes straight to full traffic and never
// reports ramping.
func TestRampDisabledClosesDirectly(t *testing.T) {
	t.Parallel()

	var ramps atomic.Int64

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{
		OnCircuitRamping: func() { ramps.Add(1) },
	},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
	)

	cb.RecordFailure()
	clk.setElapsed(2 * time.Second)
	require.NoError(t, cb.Allow())
	cb.RecordSuccess()

	assert.Equal(t, CircuitClosed, cb.State())
	assert.Equal(t, int64(0), ramps.Load(), "OnCircuitRamping never fires when disabled")
	assert.Zero(t, cb.RampRecoveryFraction())
}

// TestRampRecoveryFractionZeroWhenNotRamping verifies the gauge is 0 in every
// non-ramping state.
func TestRampRecoveryFractionZeroWhenNotRamping(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(10*time.Second),
	)

	assert.Zero(t, cb.RampRecoveryFraction(), "closed → 0")

	cb.RecordFailure()
	assert.Zero(t, cb.RampRecoveryFraction(), "open → 0")

	clk.setElapsed(2 * time.Second)
	require.NoError(t, cb.Allow())
	assert.Zero(t, cb.RampRecoveryFraction(), "half-open → 0")

	cb.RecordSuccess()
	clk.setElapsed(3 * time.Second)
	assert.InDelta(t, 0.3, cb.RampRecoveryFraction(), 1e-9, "ramping → fraction")
}

// TestRampReconfigureEnables verifies ramp recovery can be turned on at runtime
// via Reconfigure.
func TestRampReconfigureEnables(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
	)

	cb.Reconfigure(RampRecovery(10 * time.Second))
	driveToRamp(t, clk, cb)

	assert.Equal(t, CircuitRamping, cb.State())
}

// TestRampOriginMeasuredFromRampStart pins that the ramp is timed from the
// instant the breaker ENTERS the ramp (rampStart, stamped in enterRampLocked),
// not from a fixed elapsed, the trip time, or the epoch. It uses originClock,
// whose Since honours its argument, so a regression that stamps rampStart from
// the wrong source (or leaves it zero) yields a different fraction and fails.
func TestRampOriginMeasuredFromRampStart(t *testing.T) {
	t.Parallel()

	clk := &originClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(100*time.Second),
		RampInitialFraction(0), // so the curve is pure linear from 0
	)

	cb.RecordFailure()             // trip; lastFailure stamped at now
	clk.advance(2 * time.Second)   // exceed the recovery timeout
	require.NoError(t, cb.Allow()) // → half-open
	cb.RecordSuccess()             // → ramping; rampStart stamped at the current now
	require.Equal(t, CircuitRamping, cb.State())

	// Right after entry, now-rampStart ≈ 0 → fraction == initial (0). A wrong
	// origin (zero time → huge elapsed → 1.0; or lastFailure → 2s/100s = 0.02)
	// would not read 0 here.
	assert.InDelta(t, 0.0, cb.RampRecoveryFraction(), 1e-9)

	// Half the window AFTER ramp entry → linear fraction 0.5, measured from
	// rampStart (not the trip 2s earlier).
	clk.advance(50 * time.Second)
	assert.InDelta(t, 0.5, cb.RampRecoveryFraction(), 1e-9)

	// Full window since rampStart → ramp complete; the next Allow closes.
	clk.advance(50 * time.Second)
	assert.InDelta(t, 1.0, cb.RampRecoveryFraction(), 1e-9)
	require.NoError(t, cb.Allow())
	assert.Equal(t, CircuitClosed, cb.State())
}

// TestRampConcurrentAccess exercises the ramping state under the race detector.
func TestRampConcurrentAccess(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
		RampRecovery(10*time.Second),
	)

	driveToRamp(t, clk, cb)
	clk.setElapsed(5 * time.Second)

	const goroutines = 100

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()

			_ = cb.Allow()
			cb.RecordSuccess()
			_ = cb.State()
			_ = cb.RampRecoveryFraction()
		}()
	}

	wg.Wait()

	assert.Contains(t,
		[]CircuitState{CircuitClosed, CircuitOpen, CircuitHalfOpen, CircuitRamping},
		cb.State(),
	)
}

// FuzzRampFraction asserts the curve never panics and stays in [initial, 1] and
// finite for any clock position and tuning, mirroring the production clamps.
func FuzzRampFraction(f *testing.F) {
	f.Add(int64(0), int64(time.Second), 1.0, 0.1)
	f.Add(int64(time.Second/2), int64(time.Second), 2.0, 0.1)
	f.Add(int64(-5), int64(time.Second), 0.0, 0.5)

	f.Fuzz(func(t *testing.T, elapsedNS, windowNS int64, aggression, initial float64) {
		if windowNS <= 0 {
			t.Skip() // window > 0 is a precondition (rampEnabled gate)
		}

		if math.IsNaN(aggression) || math.IsInf(aggression, 0) {
			t.Skip() // a non-finite aggression cannot arrive through the option
		}

		initial = clampUnitInterval(initial) // mirror RampInitialFraction's clamp

		got := rampFraction(time.Duration(elapsedNS), time.Duration(windowNS), aggression, initial)

		require.False(t, math.IsNaN(got), "fraction must not be NaN")
		require.False(t, math.IsInf(got, 0), "fraction must be finite")
		require.GreaterOrEqual(t, got, initial, "fraction floored at initial")
		require.LessOrEqual(t, got, 1.0, "fraction never exceeds 1")
	})
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkCircuitBreakerAllow(b *testing.B) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{})

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = cb.Allow()
		}
	})
}

func BenchmarkCircuitBreakerRecordSuccess(b *testing.B) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{})

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cb.RecordSuccess()
		}
	})
}
