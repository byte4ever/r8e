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
