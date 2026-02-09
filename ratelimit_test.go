package r8e

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// rateLimitClock — controllable clock for deterministic rate limiter tests
// ---------------------------------------------------------------------------

// rateLimitClock is a fake clock for rate limiter testing. It allows explicit
// control of the current time and produces timers that can be fired manually
// or auto-fire immediately.
type rateLimitClock struct {
	mu  sync.Mutex
	now time.Time
}

func newRateLimitClock(t time.Time) *rateLimitClock {
	return &rateLimitClock{now: t}
}

func (c *rateLimitClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *rateLimitClock) Since(t time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now.Sub(t)
}

func (c *rateLimitClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func (c *rateLimitClock) NewTimer(d time.Duration) Timer {
	// For blocking mode tests: return a timer that fires after a short real
	// sleep
	// to avoid indefinite blocking in tests.
	ch := make(chan time.Time, 1)
	go func() {
		time.Sleep(1 * time.Millisecond)
		ch <- time.Now()
	}()
	return &rateLimitTimer{ch: ch}
}

type rateLimitTimer struct {
	ch chan time.Time
}

func (t *rateLimitTimer) C() <-chan time.Time      { return t.ch }
func (t *rateLimitTimer) Stop() bool               { return true }
func (t *rateLimitTimer) Reset(time.Duration) bool { return false }

// ---------------------------------------------------------------------------
// Tests: Acquire within limit succeeds
// ---------------------------------------------------------------------------

func TestRateLimiterAllowWithinLimit(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(10, clk, &Hooks{})

	// The bucket starts full with 10 tokens. Acquiring once should succeed.
	if err := rl.Allow(context.Background()); err != nil {
		t.Fatalf("Allow() = %v, want nil", err)
	}
}

func TestRateLimiterAllowMultipleWithinLimit(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(5, clk, &Hooks{})

	// 5 tokens available, acquire all 5.
	for i := range 5 {
		if err := rl.Allow(context.Background()); err != nil {
			t.Fatalf("Allow() call %d = %v, want nil", i, err)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: Exceed limit in reject mode returns ErrRateLimited
// ---------------------------------------------------------------------------

func TestRateLimiterRejectModeExceedLimit(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(3, clk, &Hooks{})

	// Drain all 3 tokens.
	for range 3 {
		if err := rl.Allow(context.Background()); err != nil {
			t.Fatalf("Allow() = %v, want nil", err)
		}
	}

	// The 4th call should be rejected.
	err := rl.Allow(context.Background())
	if err == nil {
		t.Fatal("Allow() = nil, want ErrRateLimited")
	}
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow() = %v, want ErrRateLimited", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Blocking mode waits for token
// ---------------------------------------------------------------------------

func TestRateLimiterBlockingModeWaitsForToken(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(2, clk, &Hooks{}, RateLimitBlocking())

	// Drain all tokens.
	for range 2 {
		if err := rl.Allow(context.Background()); err != nil {
			t.Fatalf("Allow() = %v, want nil", err)
		}
	}

	// In blocking mode, the next Allow should wait. Advance time in background
	// so that tokens refill.
	done := make(chan error, 1)
	go func() {
		// Advance clock so refill happens during the retry loop.
		time.Sleep(2 * time.Millisecond)
		clk.advance(1 * time.Second)
		done <- nil
	}()

	// Allow should eventually succeed after clock advances.
	err := rl.Allow(context.Background())
	if err != nil {
		t.Fatalf("Allow() in blocking mode = %v, want nil", err)
	}

	<-done
}

// ---------------------------------------------------------------------------
// Tests: Blocking mode context cancellation
// ---------------------------------------------------------------------------

func TestRateLimiterBlockingModeContextCancellation(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(1, clk, &Hooks{}, RateLimitBlocking())

	// Drain the single token.
	if err := rl.Allow(context.Background()); err != nil {
		t.Fatalf("Allow() = %v, want nil", err)
	}

	// Create a context that we cancel quickly.
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- rl.Allow(ctx)
	}()

	// Cancel after a brief moment.
	time.Sleep(5 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Allow() = nil, want context.Canceled")
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Allow() = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Allow() did not return after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// Tests: Token refill over time
// ---------------------------------------------------------------------------

func TestRateLimiterTokenRefillOverTime(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(10, clk, &Hooks{})

	// Drain all 10 tokens.
	for range 10 {
		if err := rl.Allow(context.Background()); err != nil {
			t.Fatalf("Allow() = %v, want nil", err)
		}
	}

	// No tokens left.
	if err := rl.Allow(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow() = %v, want ErrRateLimited", err)
	}

	// Advance 500ms — should refill 5 tokens (rate=10/s).
	clk.advance(500 * time.Millisecond)

	// We should be able to acquire 5 tokens now.
	for i := range 5 {
		if err := rl.Allow(context.Background()); err != nil {
			t.Fatalf("Allow() after refill, call %d = %v, want nil", i, err)
		}
	}

	// 6th should fail.
	if err := rl.Allow(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow() = %v, want ErrRateLimited", err)
	}
}

func TestRateLimiterTokenRefillCapsAtBucketCapacity(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(5, clk, &Hooks{})

	// Drain all tokens.
	for range 5 {
		_ = rl.Allow(context.Background())
	}

	// Advance 10 seconds — much more than needed to refill.
	clk.advance(10 * time.Second)

	// Should still only be able to acquire 5 (capacity cap).
	for i := range 5 {
		if err := rl.Allow(context.Background()); err != nil {
			t.Fatalf(
				"Allow() after long refill, call %d = %v, want nil",
				i,
				err,
			)
		}
	}

	// 6th should fail — tokens capped at 5.
	if err := rl.Allow(context.Background()); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow() = %v, want ErrRateLimited (capped at capacity)", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Saturated() returns true when empty, false when tokens available
// ---------------------------------------------------------------------------

func TestRateLimiterSaturatedWhenEmpty(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(2, clk, &Hooks{})

	// Not saturated initially.
	if rl.Saturated() {
		t.Fatal("Saturated() = true, want false on fresh limiter")
	}

	// Drain all tokens.
	_ = rl.Allow(context.Background())
	_ = rl.Allow(context.Background())

	// Now saturated.
	if !rl.Saturated() {
		t.Fatal("Saturated() = false, want true after draining all tokens")
	}

	// Refill by advancing time.
	clk.advance(1 * time.Second)

	// No longer saturated.
	if rl.Saturated() {
		t.Fatal("Saturated() = true, want false after refill")
	}
}

// ---------------------------------------------------------------------------
// Tests: Hook emission on rejection
// ---------------------------------------------------------------------------

func TestRateLimiterHookEmissionOnRejection(t *testing.T) {
	clk := newRateLimitClock(time.Now())

	var rateLimitedCount atomic.Int64
	hooks := &Hooks{
		OnRateLimited: func() { rateLimitedCount.Add(1) },
	}

	rl := NewRateLimiter(1, clk, hooks)

	// Drain the token.
	_ = rl.Allow(context.Background())

	// Rejected — hook should fire.
	_ = rl.Allow(context.Background())
	if got := rateLimitedCount.Load(); got != 1 {
		t.Fatalf("OnRateLimited called %d times, want 1", got)
	}

	// Rejected again.
	_ = rl.Allow(context.Background())
	if got := rateLimitedCount.Load(); got != 2 {
		t.Fatalf("OnRateLimited called %d times, want 2", got)
	}
}

func TestRateLimiterNoHookOnBlockingSuccess(t *testing.T) {
	clk := newRateLimitClock(time.Now())

	var rateLimitedCount atomic.Int64
	hooks := &Hooks{
		OnRateLimited: func() { rateLimitedCount.Add(1) },
	}

	rl := NewRateLimiter(1, clk, hooks, RateLimitBlocking())

	// Drain the token.
	_ = rl.Allow(context.Background())

	// Advance clock so the blocking wait finds tokens.
	go func() {
		time.Sleep(2 * time.Millisecond)
		clk.advance(1 * time.Second)
	}()

	// Blocking Allow should succeed without emitting hook.
	err := rl.Allow(context.Background())
	if err != nil {
		t.Fatalf("Allow() = %v, want nil", err)
	}

	if got := rateLimitedCount.Load(); got != 0 {
		t.Fatalf(
			"OnRateLimited called %d times, want 0 for blocking success",
			got,
		)
	}
}

// ---------------------------------------------------------------------------
// Tests: Nil hooks do not panic
// ---------------------------------------------------------------------------

func TestRateLimiterNilHooksDoNotPanic(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(1, clk, &Hooks{})

	// Drain.
	_ = rl.Allow(context.Background())
	// This should not panic even with nil OnRateLimited hook.
	_ = rl.Allow(context.Background())
}

// ---------------------------------------------------------------------------
// Tests: RateLimitOption constructors
// ---------------------------------------------------------------------------

func TestRateLimitBlockingOption(t *testing.T) {
	var cfg rateLimitConfig
	RateLimitBlocking()(&cfg)
	if !cfg.blocking {
		t.Fatal("blocking = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Tests: Concurrent access (50 goroutines)
// ---------------------------------------------------------------------------

func TestRateLimiterConcurrentAccess(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{})

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	var allowed atomic.Int64
	var rejected atomic.Int64

	for range goroutines {
		go func() {
			defer wg.Done()
			for range 10 {
				err := rl.Allow(context.Background())
				if err == nil {
					allowed.Add(1)
				} else {
					rejected.Add(1)
				}
			}
			_ = rl.Saturated()
		}()
	}

	wg.Wait()

	total := allowed.Load() + rejected.Load()
	if total != 500 {
		t.Fatalf("total calls = %d, want 500", total)
	}

	// With 100 tokens and 500 calls, we should have some allowed and some
	// rejected.
	if allowed.Load() == 0 {
		t.Fatal("expected some calls to be allowed")
	}
	if rejected.Load() == 0 {
		t.Fatal("expected some calls to be rejected (500 calls > 100 tokens)")
	}
}

func TestRateLimiterConcurrentAccessWithRefill(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(1000, clk, &Hooks{})

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			_ = rl.Allow(context.Background())
			_ = rl.Saturated()
		}()
	}

	wg.Wait()
	// Just verify no panics or races.
}

// ---------------------------------------------------------------------------
// Tests: Blocking mode with already-cancelled context (pre-sleep check)
// ---------------------------------------------------------------------------

func TestRateLimiterBlockingModeAlreadyCancelledContext(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(1, clk, &Hooks{}, RateLimitBlocking())

	// Drain the token.
	_ = rl.Allow(context.Background())

	// Pass an already-cancelled context — should return immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling Allow

	err := rl.Allow(ctx)
	if err == nil {
		t.Fatal("Allow() = nil, want context.Canceled")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Allow() = %v, want context.Canceled", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Tiny rate where addTokens rounds to zero
// ---------------------------------------------------------------------------

func TestRateLimiterTinyRateRefill(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	// Use an incredibly small rate. With rate = 1e-18, even 1ns elapsed gives
	// addTokens = 1 * 1e-18 which truncates to 0.
	rl := NewRateLimiter(1e-18, clk, &Hooks{})

	// Drain the (extremely tiny) bucket — capacity is int64(1e-18 * 1e9) = 0,
	// so the bucket starts with 0 tokens. Any Allow should fail.
	err := rl.Allow(context.Background())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow() with tiny rate = %v, want ErrRateLimited", err)
	}

	// Advance a tiny amount — refill should compute addTokens = 0.
	clk.advance(1 * time.Nanosecond)
	err = rl.Allow(context.Background())
	if !errors.Is(err, ErrRateLimited) {
		t.Fatalf("Allow() after tiny advance = %v, want ErrRateLimited", err)
	}
}

// ---------------------------------------------------------------------------
// Tests: Concurrent refill contention (exercises CAS retry paths)
// ---------------------------------------------------------------------------

func TestRateLimiterConcurrentRefillContention(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(10000, clk, &Hooks{})

	// Drain all tokens.
	for {
		if err := rl.Allow(context.Background()); err != nil {
			break
		}
	}

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	// All goroutines advance the clock and call refill + Allow concurrently.
	// This creates heavy contention on the CAS loop.
	for range goroutines {
		go func() {
			defer wg.Done()
			clk.advance(10 * time.Millisecond)
			for range 20 {
				_ = rl.Allow(context.Background())
				_ = rl.Saturated()
			}
		}()
	}

	wg.Wait()
	// Just verify no panics or races.
}

// ---------------------------------------------------------------------------
// Tests: Blocking mode with context deadline exceeded
// ---------------------------------------------------------------------------

func TestRateLimiterBlockingModeContextDeadlineExceeded(t *testing.T) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(1, clk, &Hooks{}, RateLimitBlocking())

	// Drain the token.
	_ = rl.Allow(context.Background())

	// Use a context with a very short deadline.
	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Millisecond,
	)
	defer cancel()

	err := rl.Allow(ctx)
	if err == nil {
		t.Fatal("Allow() = nil, want context deadline exceeded")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Allow() = %v, want context.DeadlineExceeded", err)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkRateLimiterAllow(b *testing.B) {
	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(
		1e9,
		clk,
		&Hooks{},
	) // very high rate so tokens never run out
	ctx := context.Background()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = rl.Allow(ctx)
		}
	})
}
