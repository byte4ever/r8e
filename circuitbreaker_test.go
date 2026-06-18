package r8e

import (
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
