package r8e

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// stubClock — controllable clock for deterministic circuit breaker tests
// ---------------------------------------------------------------------------

type stubClock struct {
	now     time.Time
	elapsed time.Duration // returned by Since, regardless of argument
}

func (c *stubClock) Now() time.Time                { return c.now }
func (c *stubClock) Since(time.Time) time.Duration { return c.elapsed }
func (c *stubClock) NewTimer(d time.Duration) Timer {
	return &fakeTimer{}
}

// setElapsed sets the exact elapsed duration returned by Since.
func (c *stubClock) setElapsed(d time.Duration) {
	c.elapsed = d
}

// ---------------------------------------------------------------------------
// Default config values
// ---------------------------------------------------------------------------

func TestCircuitBreakerDefaultConfig(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{})

	// Verify defaults by exercising the breaker.
	// Default threshold is 5 — four failures should keep it closed.
	for range 4 {
		cb.RecordFailure()
	}
	if err := cb.Allow(); err != nil {
		t.Fatalf("Allow() after 4 failures = %v, want nil (threshold is 5)", err)
	}

	// The 5th failure should open it.
	cb.RecordFailure()
	if err := cb.Allow(); err != ErrCircuitOpen {
		t.Fatalf("Allow() after 5 failures = %v, want ErrCircuitOpen", err)
	}
}

// ---------------------------------------------------------------------------
// Custom config values
// ---------------------------------------------------------------------------

func TestCircuitBreakerCustomConfig(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(2),
		RecoveryTimeout(10*time.Second),
		HalfOpenMaxAttempts(3),
	)

	// Two failures open the circuit (custom threshold = 2).
	cb.RecordFailure()
	cb.RecordFailure()
	if err := cb.Allow(); err != ErrCircuitOpen {
		t.Fatalf("Allow() after 2 failures = %v, want ErrCircuitOpen", err)
	}

	// Advance past custom recovery timeout (10s).
	clk.setElapsed(11 * time.Second)
	if err := cb.Allow(); err != nil {
		t.Fatalf("Allow() after recovery timeout = %v, want nil (half-open)", err)
	}
	if got := cb.State(); got != "half_open" {
		t.Fatalf("State() = %q, want %q", got, "half_open")
	}

	// Half-open: need 3 successes to close (custom halfOpenMaxAttempts = 3).
	cb.RecordSuccess()
	if got := cb.State(); got != "half_open" {
		t.Fatalf("State() after 1 success in half-open = %q, want %q", got, "half_open")
	}
	cb.RecordSuccess()
	if got := cb.State(); got != "half_open" {
		t.Fatalf("State() after 2 successes in half-open = %q, want %q", got, "half_open")
	}
	cb.RecordSuccess()
	if got := cb.State(); got != "closed" {
		t.Fatalf("State() after 3 successes in half-open = %q, want %q", got, "closed")
	}
}

// ---------------------------------------------------------------------------
// Closed state: allows calls
// ---------------------------------------------------------------------------

func TestClosedStateAllowsCalls(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{})

	if err := cb.Allow(); err != nil {
		t.Fatalf("Allow() on fresh breaker = %v, want nil", err)
	}
	if got := cb.State(); got != "closed" {
		t.Fatalf("State() = %q, want %q", got, "closed")
	}
}

// ---------------------------------------------------------------------------
// Closed state: counts failures and opens at threshold
// ---------------------------------------------------------------------------

func TestClosedStateOpensAtThreshold(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{}, FailureThreshold(3))

	cb.RecordFailure()
	cb.RecordFailure()

	// Still closed after 2 failures (threshold is 3).
	if got := cb.State(); got != "closed" {
		t.Fatalf("State() after 2 failures = %q, want %q", got, "closed")
	}

	cb.RecordFailure()

	// Now open.
	if got := cb.State(); got != "open" {
		t.Fatalf("State() after 3 failures = %q, want %q", got, "open")
	}
}

// ---------------------------------------------------------------------------
// Open state: rejects with ErrCircuitOpen
// ---------------------------------------------------------------------------

func TestOpenStateRejectsWithErrCircuitOpen(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{}, FailureThreshold(1))

	cb.RecordFailure()

	err := cb.Allow()
	if err != ErrCircuitOpen {
		t.Fatalf("Allow() in open state = %v, want ErrCircuitOpen", err)
	}
}

// ---------------------------------------------------------------------------
// Open to half-open: after recovery timeout
// ---------------------------------------------------------------------------

func TestOpenToHalfOpenAfterRecoveryTimeout(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(5*time.Second),
	)

	cb.RecordFailure()

	// Still within recovery timeout.
	clk.setElapsed(4 * time.Second)
	if err := cb.Allow(); err != ErrCircuitOpen {
		t.Fatalf("Allow() before recovery timeout = %v, want ErrCircuitOpen", err)
	}

	// Past recovery timeout.
	clk.setElapsed(6 * time.Second)
	if err := cb.Allow(); err != nil {
		t.Fatalf("Allow() after recovery timeout = %v, want nil", err)
	}
	if got := cb.State(); got != "half_open" {
		t.Fatalf("State() = %q, want %q", got, "half_open")
	}
}

// ---------------------------------------------------------------------------
// Half-open success: closes circuit
// ---------------------------------------------------------------------------

func TestHalfOpenSuccessClosesCircuit(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
	)

	cb.RecordFailure()
	clk.setElapsed(2 * time.Second)

	// Transition to half-open.
	if err := cb.Allow(); err != nil {
		t.Fatalf("Allow() = %v, want nil", err)
	}

	cb.RecordSuccess()

	if got := cb.State(); got != "closed" {
		t.Fatalf("State() after success in half-open = %q, want %q", got, "closed")
	}
}

// ---------------------------------------------------------------------------
// Half-open failure: reopens circuit
// ---------------------------------------------------------------------------

func TestHalfOpenFailureReopensCircuit(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
	)

	cb.RecordFailure()
	clk.setElapsed(2 * time.Second)

	// Transition to half-open.
	if err := cb.Allow(); err != nil {
		t.Fatalf("Allow() = %v, want nil", err)
	}

	cb.RecordFailure()

	if got := cb.State(); got != "open" {
		t.Fatalf("State() after failure in half-open = %q, want %q", got, "open")
	}
}

// ---------------------------------------------------------------------------
// Success in closed state resets failure count
// ---------------------------------------------------------------------------

func TestSuccessInClosedStateResetsFailureCount(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{}, FailureThreshold(3))

	cb.RecordFailure()
	cb.RecordFailure()
	// 2 failures. A success should reset the count.
	cb.RecordSuccess()

	// Now record 2 more failures — should NOT open (count was reset).
	cb.RecordFailure()
	cb.RecordFailure()
	if got := cb.State(); got != "closed" {
		t.Fatalf("State() = %q, want %q after reset and 2 failures", got, "closed")
	}

	// The 3rd failure after reset should open.
	cb.RecordFailure()
	if got := cb.State(); got != "open" {
		t.Fatalf("State() = %q, want %q", got, "open")
	}
}

// ---------------------------------------------------------------------------
// State() returns correct strings
// ---------------------------------------------------------------------------

func TestStateReturnsCorrectStrings(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
	)

	if got := cb.State(); got != "closed" {
		t.Fatalf("State() = %q, want %q", got, "closed")
	}

	cb.RecordFailure()
	if got := cb.State(); got != "open" {
		t.Fatalf("State() = %q, want %q", got, "open")
	}

	clk.setElapsed(2 * time.Second)
	cb.Allow() // triggers half-open
	if got := cb.State(); got != "half_open" {
		t.Fatalf("State() = %q, want %q", got, "half_open")
	}
}

// ---------------------------------------------------------------------------
// Hook emissions
// ---------------------------------------------------------------------------

func TestCircuitBreakerHookEmissions(t *testing.T) {
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
	if got := openCount.Load(); got != 1 {
		t.Fatalf("OnCircuitOpen called %d times, want 1", got)
	}

	// Trigger half-open.
	clk.setElapsed(2 * time.Second)
	cb.Allow()
	if got := halfOpenCount.Load(); got != 1 {
		t.Fatalf("OnCircuitHalfOpen called %d times, want 1", got)
	}

	// Trigger close.
	cb.RecordSuccess()
	if got := closeCount.Load(); got != 1 {
		t.Fatalf("OnCircuitClose called %d times, want 1", got)
	}
}

func TestCircuitBreakerHookOnReopenFromHalfOpen(t *testing.T) {
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
	if got := openCount.Load(); got != 1 {
		t.Fatalf("OnCircuitOpen called %d times, want 1", got)
	}

	// Half-open.
	clk.setElapsed(2 * time.Second)
	cb.Allow()

	// Failure in half-open should re-open and fire hook again.
	cb.RecordFailure()
	if got := openCount.Load(); got != 2 {
		t.Fatalf("OnCircuitOpen called %d times, want 2 (reopened from half-open)", got)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access: 100 goroutines doing Allow/RecordSuccess/RecordFailure
// ---------------------------------------------------------------------------

func TestCircuitBreakerConcurrentAccess(t *testing.T) {
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

	// Just verify it didn't panic or race — the race detector will catch issues.
	state := cb.State()
	if state != "closed" && state != "open" && state != "half_open" {
		t.Fatalf("State() = %q, want one of closed/open/half_open", state)
	}
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
