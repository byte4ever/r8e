package r8e

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(10, clk, &Hooks{})

	// The bucket starts full with 10 tokens. Acquiring once should succeed.
	require.NoError(t, rl.Allow(context.Background()))
}

func TestRateLimiterAllowMultipleWithinLimit(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(5, clk, &Hooks{})

	// 5 tokens available, acquire all 5.
	for range 5 {
		require.NoError(t, rl.Allow(context.Background()))
	}
}

// ---------------------------------------------------------------------------
// Tests: Exceed limit in reject mode returns ErrRateLimited
// ---------------------------------------------------------------------------

func TestRateLimiterRejectModeExceedLimit(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(3, clk, &Hooks{})

	// Drain all 3 tokens.
	for range 3 {
		require.NoError(t, rl.Allow(context.Background()))
	}

	// The 4th call should be rejected.
	err := rl.Allow(context.Background())
	require.Error(t, err)
	require.ErrorIs(t, err, ErrRateLimited)
}

// ---------------------------------------------------------------------------
// Tests: Blocking mode waits for token
// ---------------------------------------------------------------------------

func TestRateLimiterBlockingModeWaitsForToken(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(2, clk, &Hooks{}, RateLimitBlocking())

	// Drain all tokens.
	for range 2 {
		require.NoError(t, rl.Allow(context.Background()))
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
	require.NoError(t, rl.Allow(context.Background()))

	<-done
}

// ---------------------------------------------------------------------------
// Tests: Blocking mode context cancellation
// ---------------------------------------------------------------------------

func TestRateLimiterBlockingModeContextCancellation(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(1, clk, &Hooks{}, RateLimitBlocking())

	// Drain the single token.
	require.NoError(t, rl.Allow(context.Background()))

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
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Allow() did not return after context cancellation")
	}
}

// ---------------------------------------------------------------------------
// Tests: Token refill over time
// ---------------------------------------------------------------------------

func TestRateLimiterTokenRefillOverTime(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(10, clk, &Hooks{})

	// Drain all 10 tokens.
	for range 10 {
		require.NoError(t, rl.Allow(context.Background()))
	}

	// No tokens left.
	require.ErrorIs(t, rl.Allow(context.Background()), ErrRateLimited)

	// Advance 500ms — should refill 5 tokens (rate=10/s).
	clk.advance(500 * time.Millisecond)

	// We should be able to acquire 5 tokens now.
	for range 5 {
		require.NoError(t, rl.Allow(context.Background()))
	}

	// 6th should fail.
	require.ErrorIs(t, rl.Allow(context.Background()), ErrRateLimited)
}

func TestRateLimiterTokenRefillCapsAtBucketCapacity(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(5, clk, &Hooks{})

	// Drain all tokens.
	for range 5 {
		_ = rl.Allow(context.Background())
	}

	// Advance 10 seconds — much more than needed to refill.
	clk.advance(10 * time.Second)

	// Should still only be able to acquire 5 (capacity cap).
	for range 5 {
		require.NoError(t, rl.Allow(context.Background()))
	}

	// 6th should fail — tokens capped at 5.
	require.ErrorIs(t, rl.Allow(context.Background()), ErrRateLimited)
}

// ---------------------------------------------------------------------------
// Tests: Saturated() returns true when empty, false when tokens available
// ---------------------------------------------------------------------------

func TestRateLimiterSaturatedWhenEmpty(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(2, clk, &Hooks{})

	// Not saturated initially.
	require.False(t, rl.Saturated())

	// Drain all tokens.
	_ = rl.Allow(context.Background())
	_ = rl.Allow(context.Background())

	// Now saturated.
	require.True(t, rl.Saturated())

	// Refill by advancing time.
	clk.advance(1 * time.Second)

	// No longer saturated.
	require.False(t, rl.Saturated())
}

// ---------------------------------------------------------------------------
// Tests: Hook emission on rejection
// ---------------------------------------------------------------------------

func TestRateLimiterHookEmissionOnRejection(t *testing.T) {
	t.Parallel()

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
	require.Equal(t, int64(1), rateLimitedCount.Load())

	// Rejected again.
	_ = rl.Allow(context.Background())
	require.Equal(t, int64(2), rateLimitedCount.Load())
}

func TestRateLimiterNoHookOnBlockingSuccess(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, rl.Allow(context.Background()))

	require.Equal(t, int64(0), rateLimitedCount.Load())
}

// ---------------------------------------------------------------------------
// Tests: Nil hooks do not panic
// ---------------------------------------------------------------------------

func TestRateLimiterNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(1, clk, nil) // nil *Hooks must be a no-op

	// Drain.
	_ = rl.Allow(context.Background())
	// This should not panic even with a nil Hooks.
	_ = rl.Allow(context.Background())
}

// ---------------------------------------------------------------------------
// Tests: RateLimitOption constructors
// ---------------------------------------------------------------------------

func TestRateLimitBlockingOption(t *testing.T) {
	t.Parallel()

	var cfg rateLimitConfig
	RateLimitBlocking()(&cfg)
	require.True(t, cfg.blocking)
}

// ---------------------------------------------------------------------------
// Tests: Concurrent access (50 goroutines)
// ---------------------------------------------------------------------------

func TestRateLimiterConcurrentAccess(t *testing.T) {
	t.Parallel()

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
	require.Equal(t, int64(500), total)

	// With 100 tokens and 500 calls, we should have some allowed and some
	// rejected.
	require.NotZero(t, allowed.Load())
	require.NotZero(t, rejected.Load())
}

func TestRateLimiterConcurrentAccessWithRefill(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(1, clk, &Hooks{}, RateLimitBlocking())

	// Drain the token.
	_ = rl.Allow(context.Background())

	// Pass an already-cancelled context — should return immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling Allow

	err := rl.Allow(ctx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)
}

// ---------------------------------------------------------------------------
// Tests: Tiny rate where addTokens rounds to zero
// ---------------------------------------------------------------------------

func TestRateLimiterTinyRateRefill(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	// Use an incredibly small rate. With rate = 1e-18, even 1ns elapsed gives
	// addTokens = 1 * 1e-18 which truncates to 0.
	rl := NewRateLimiter(1e-18, clk, &Hooks{})

	// Drain the (extremely tiny) bucket — capacity is int64(1e-18 * 1e9) = 0,
	// so the bucket starts with 0 tokens. Any Allow should fail.
	require.ErrorIs(t, rl.Allow(context.Background()), ErrRateLimited)

	// Advance a tiny amount — refill should compute addTokens = 0.
	clk.advance(1 * time.Nanosecond)
	require.ErrorIs(t, rl.Allow(context.Background()), ErrRateLimited)
}

// ---------------------------------------------------------------------------
// Tests: Concurrent refill contention (exercises CAS retry paths)
// ---------------------------------------------------------------------------

func TestRateLimiterConcurrentRefillContention(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

// ---------------------------------------------------------------------------
// Tests: AIMD adaptive rate
// ---------------------------------------------------------------------------

func TestRateLimiterRecordOutcomeNoAIMDIsNoOp(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(10, clk, &Hooks{})

	require.Nil(t, rl.aimd)

	// RecordOutcome must be a harmless no-op on a non-AIMD limiter.
	rl.RecordOutcome(ErrRateLimited)
	rl.RecordOutcome(nil)

	require.InDelta(t, 10.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterAIMDBackoffSequenceAndFloor(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDMinRate(10), AIMDBackoff(0.5), AIMDInterval(time.Second)))

	// Before the first interval elapses, an overload is absorbed (no change).
	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 100.0, rl.CurrentRate(), 1e-9)

	// One decrease per interval, clamped at the 10/s floor.
	for _, want := range []float64{50, 25, 12.5, 10, 10} {
		clk.advance(time.Second)
		rl.RecordOutcome(ErrRateLimited)
		require.InDelta(t, want, rl.CurrentRate(), 1e-9)
	}
}

func TestRateLimiterAIMDRecoverySequenceAndCeiling(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDMaxRate(200), AIMDIncrease(40), AIMDInterval(time.Second)))

	// One additive step per clean interval, clamped at the 200/s ceiling.
	for _, want := range []float64{140, 180, 200, 200} {
		clk.advance(time.Second)
		rl.RecordOutcome(nil)
		require.InDelta(t, want, rl.CurrentRate(), 1e-9)
	}
}

func TestRateLimiterAIMDBurstWithinIntervalDecreasesOnce(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDMinRate(1), AIMDBackoff(0.5), AIMDInterval(time.Second)))

	clk.advance(time.Second)
	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 50.0, rl.CurrentRate(), 1e-9)

	// A burst of further overloads inside the same interval is absorbed.
	rl.RecordOutcome(ErrRateLimited)
	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 50.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterAIMDPinnedSuccessDoesNotConsumeInterval(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	// maxRate defaults to the base rate (100); increase default is 5.
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDBackoff(0.5), AIMDInterval(time.Second)))

	clk.advance(time.Second)

	// At the ceiling a success leaves the rate unchanged AND does not stamp the
	// interval, so an overload at the same instant still fires immediately.
	rl.RecordOutcome(nil)
	require.InDelta(t, 100.0, rl.CurrentRate(), 1e-9)

	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 50.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterAIMDHookFiresWithNewRate(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())

	var adapted []float64

	hooks := &Hooks{OnRateAdapted: func(rate float64) {
		adapted = append(adapted, rate)
	}}
	rl := NewRateLimiter(100, clk, hooks,
		AIMD(AIMDBackoff(0.5), AIMDInterval(time.Second)))

	// No interval elapsed → no adaptation → no hook.
	rl.RecordOutcome(ErrRateLimited)
	require.Empty(t, adapted)

	clk.advance(time.Second)
	rl.RecordOutcome(ErrRateLimited)
	require.Equal(t, []float64{50}, adapted)
}

func TestRateLimiterAIMDHookFiresOnBothDirections(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())

	var adapted []float64

	hooks := &Hooks{OnRateAdapted: func(rate float64) {
		adapted = append(adapted, rate)
	}}
	// Ceiling above the base so the recovery direction can also move the rate.
	rl := NewRateLimiter(100, clk, hooks,
		AIMD(AIMDMaxRate(200), AIMDBackoff(0.5), AIMDIncrease(40), AIMDInterval(time.Second)))

	clk.advance(time.Second)
	rl.RecordOutcome(ErrRateLimited) // backoff: 100 → 50

	clk.advance(time.Second)
	rl.RecordOutcome(nil) // recovery: 50 → 90

	require.Equal(t, []float64{50, 90}, adapted) // hook fires for both directions
}

func TestRateLimiterAIMDCustomClassifier(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	sentinel := errors.New("overload")
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(
			AIMDBackoff(0.5),
			AIMDInterval(time.Second),
			AIMDClassifier(func(err error) bool { return errors.Is(err, sentinel) }),
		))

	// ErrRateLimited is not overload under the custom classifier → treated as a
	// clean interval; the rate is already at its ceiling so it stays put.
	clk.advance(time.Second)
	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 100.0, rl.CurrentRate(), 1e-9)

	// The sentinel is overload → multiplicative decrease.
	clk.advance(time.Second)
	rl.RecordOutcome(sentinel)
	require.InDelta(t, 50.0, rl.CurrentRate(), 1e-9)
}

func TestDefaultAIMDOverload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil is not overload", nil, false},
		{"ErrRateLimited is overload", ErrRateLimited, true},
		{
			"retry-after hint is overload",
			RetryAfterError(errors.New("x"), time.Second),
			true,
		},
		{
			"wrapped retry-after hint is overload",
			fmt.Errorf("ctx: %w", RetryAfterError(errors.New("x"), time.Second)),
			true,
		},
		{
			"wrapped ErrRateLimited is overload",
			fmt.Errorf("ctx: %w", ErrRateLimited),
			true,
		},
		{"plain error is not overload", errors.New("plain"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, defaultAIMDOverload(tt.err))
		})
	}
}

func TestAIMDConfigResolve(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                                string
		in                                  aimdConfig
		base                                float64
		backoff, interval, maxR, minR, incr float64
	}{
		{
			name:     "defaults derived from base",
			in:       aimdConfig{},
			base:     100,
			backoff:  defaultAIMDBackoff,
			interval: float64(defaultAIMDInterval),
			maxR:     100,
			minR:     10,
			incr:     5,
		},
		{
			name:     "invalid values reset, minRate clamps to maxRate",
			in:       aimdConfig{minRate: 500, maxRate: 100, backoff: 1.5, interval: -1},
			base:     100,
			backoff:  defaultAIMDBackoff,
			interval: float64(defaultAIMDInterval),
			maxR:     100,
			minR:     100,
			incr:     5,
		},
		{
			name:     "backoff at exclusive lower bound resets",
			in:       aimdConfig{backoff: 0},
			base:     200,
			backoff:  defaultAIMDBackoff,
			interval: float64(defaultAIMDInterval),
			maxR:     200,
			minR:     20,
			incr:     10,
		},
		{
			name:     "non-positive increase resets to base/20",
			in:       aimdConfig{increase: -7},
			base:     100,
			backoff:  defaultAIMDBackoff,
			interval: float64(defaultAIMDInterval),
			maxR:     100,
			minR:     10,
			incr:     5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := tt.in
			cfg.resolve(tt.base)

			require.InDelta(t, tt.backoff, cfg.backoff, 1e-9)
			require.Equal(t, time.Duration(tt.interval), cfg.interval)
			require.InDelta(t, tt.maxR, cfg.maxRate, 1e-9)
			require.InDelta(t, tt.minR, cfg.minRate, 1e-9)
			require.InDelta(t, tt.incr, cfg.increase, 1e-9)
			require.NotNil(t, cfg.classifier)
		})
	}
}

func TestRateLimiterReconfigureAIMD(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDBackoff(0.5), AIMDInterval(2*time.Second)))

	// Overlay a gentler backoff and a floor; the interval is left unset and must
	// be preserved at its current 2s.
	require.NoError(t, rl.ReconfigureAIMD(AIMDBackoff(0.9), AIMDMinRate(50)))

	// Within the preserved 2s interval → no change.
	clk.advance(time.Second)
	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 100.0, rl.CurrentRate(), 1e-9)

	// Past 2s → the new 0.9 factor applies.
	clk.advance(time.Second)
	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 90.0, rl.CurrentRate(), 1e-9)

	// The new 50/s floor stops further decreases below it.
	for range 20 {
		clk.advance(2 * time.Second)
		rl.RecordOutcome(ErrRateLimited)
	}

	require.InDelta(t, 50.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterReconfigureAIMDWithoutAIMD(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{})

	require.ErrorIs(t, rl.ReconfigureAIMD(AIMDBackoff(0.5)), ErrAIMDWithoutRateLimit)
}

func TestRateLimiterAIMDConcurrentRecordOutcome(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDMinRate(1), AIMDBackoff(0.5), AIMDInterval(time.Second)))

	clk.advance(time.Second)

	const goroutines = 50

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			rl.RecordOutcome(ErrRateLimited)
		}()
	}

	wg.Wait()

	// All overloads fall in one interval → exactly one decrease (race-free).
	require.InDelta(t, 50.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterAIMDConcurrentMixedDecreasesOnce(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDMinRate(1), AIMDBackoff(0.5), AIMDInterval(time.Second)))

	clk.advance(time.Second)

	const goroutines = 50

	var wg sync.WaitGroup

	wg.Add(goroutines)

	// Half overloads, half successes, all in one interval. Successes at the
	// ceiling are no-ops that never stamp the interval, so the single decrease
	// (the first overload past the gate) wins deterministically.
	for i := range goroutines {
		overload := i%2 == 0

		go func() {
			defer wg.Done()

			if overload {
				rl.RecordOutcome(ErrRateLimited)
			} else {
				rl.RecordOutcome(nil)
			}
		}()
	}

	wg.Wait()

	require.InDelta(t, 50.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterAIMDIntervalGateBoundary(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDBackoff(0.5), AIMDInterval(time.Second)))

	// One nanosecond short of the interval: the gate is still closed.
	clk.advance(time.Second - time.Nanosecond)
	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 100.0, rl.CurrentRate(), 1e-9)

	// The final nanosecond reaches exactly the interval: the gate opens.
	clk.advance(time.Nanosecond)
	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 50.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterAIMDAdaptationShrinksAdmission(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDMinRate(10), AIMDBackoff(0.5), AIMDInterval(time.Second)))

	// Drive the rate to its 10/s floor.
	for range 6 {
		clk.advance(time.Second)
		rl.RecordOutcome(ErrRateLimited)
	}

	require.InDelta(t, 10.0, rl.CurrentRate(), 1e-9)

	// The bucket capacity shrank with the rate: far fewer than the original 100
	// tokens are admittable before rejection — the adaptation changes observable
	// admission, not just the reported scalar.
	allowed := 0

	for range 100 {
		if rl.Allow(context.Background()) != nil {
			break
		}

		allowed++
	}

	require.LessOrEqual(t, allowed, 11)
	require.Positive(t, allowed)
}

func TestRateLimiterReconfigureAIMDPreservesUnsetFields(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	// Ceiling above the base so additive recovery has room; explicit increase 40.
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDMaxRate(200), AIMDIncrease(40), AIMDInterval(time.Second)))

	// Overlay only the backoff; increase (40) and maxRate (200) must be preserved.
	require.NoError(t, rl.ReconfigureAIMD(AIMDBackoff(0.9)))

	clk.advance(time.Second)
	rl.RecordOutcome(nil) // success → additive recovery using the preserved step
	require.InDelta(t, 140.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterReconfigureAIMDPreservesClassifierAndFloor(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	sentinel := errors.New("overload")
	// Custom classifier (sentinel-only) + explicit floor 40; overlay only backoff.
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(
			AIMDMinRate(40),
			AIMDBackoff(0.5),
			AIMDInterval(time.Second),
			AIMDClassifier(func(err error) bool { return errors.Is(err, sentinel) }),
		))

	require.NoError(t, rl.ReconfigureAIMD(AIMDBackoff(0.9)))

	// The preserved custom classifier still governs: ErrRateLimited is NOT overload
	// (the default classifier would have treated it as one), so it recovers, not backs off.
	clk.advance(time.Second)
	rl.RecordOutcome(ErrRateLimited)
	require.InDelta(t, 100.0, rl.CurrentRate(), 1e-9) // at ceiling, success is a no-op

	// The sentinel is overload under the preserved classifier → 0.9 backoff applies.
	clk.advance(time.Second)
	rl.RecordOutcome(sentinel)
	require.InDelta(t, 90.0, rl.CurrentRate(), 1e-9)

	// The preserved 40/s floor stops further decreases below it.
	for range 20 {
		clk.advance(time.Second)
		rl.RecordOutcome(sentinel)
	}

	require.InDelta(t, 40.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterReconfigureAIMDRaisesCeiling(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDIncrease(40), AIMDInterval(time.Second))) // maxRate defaults to 100

	// At the original ceiling, a success cannot climb.
	clk.advance(time.Second)
	rl.RecordOutcome(nil)
	require.InDelta(t, 100.0, rl.CurrentRate(), 1e-9)

	// Raise the ceiling; now recovery can climb past the old 100.
	require.NoError(t, rl.ReconfigureAIMD(AIMDMaxRate(300)))

	clk.advance(time.Second)
	rl.RecordOutcome(nil)
	require.InDelta(t, 140.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterReconfigureAIMDResetsInvalidField(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDMaxRate(300), AIMDIncrease(40), AIMDInterval(time.Second)))

	// An explicit non-positive increase resets to the default (maxRate/20 = 15),
	// not to the contaminated -1 — the clamp-on-invalid contract holds on reconfigure.
	require.NoError(t, rl.ReconfigureAIMD(AIMDIncrease(-1)))

	clk.advance(time.Second)
	rl.RecordOutcome(nil)
	require.InDelta(t, 115.0, rl.CurrentRate(), 1e-9)
}

func TestRateLimiterReconfigureAIMDZeroMaxRateDoesNotStarve(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{},
		AIMD(AIMDMinRate(10), AIMDBackoff(0.5), AIMDInterval(time.Second)))

	// A non-positive ceiling must reset to the base rate (100), not pin the
	// controller to 0 — the clamp-on-invalid contract must hold on reconfigure.
	require.NoError(t, rl.ReconfigureAIMD(AIMDMaxRate(0)))
	require.InDelta(t, 100.0, rl.CurrentRate(), 1e-9)

	// The controller still adapts normally and never starves to 0.
	for range 10 {
		clk.advance(time.Second)
		rl.RecordOutcome(ErrRateLimited)
	}

	require.InDelta(t, 10.0, rl.CurrentRate(), 1e-9) // rests at the floor, not 0
}

// ---------------------------------------------------------------------------
// Tests: AIMD via config (BuildOptions / Reconfigure)
// ---------------------------------------------------------------------------

func TestBuildOptionsAIMDWithoutRateLimit(t *testing.T) {
	t.Parallel()

	cfg := PolicyConfig{AIMD: &AIMDConfig{}}

	_, err := BuildOptions(&cfg)
	require.ErrorIs(t, err, ErrAIMDWithoutRateLimit)
}

func TestBuildOptionsAIMD(t *testing.T) {
	t.Parallel()

	cfg := PolicyConfig{
		RateLimit: f64Ptr(100),
		AIMD: &AIMDConfig{
			MinRate:  f64Ptr(10),
			MaxRate:  f64Ptr(150),
			Backoff:  f64Ptr(0.8),
			Increase: f64Ptr(5),
			Interval: strPtr("500ms"),
		},
	}

	opts, err := BuildOptions(&cfg)
	require.NoError(t, err)

	p := NewPolicy[string]("aimd-cfg", opts...)
	require.NotNil(t, p.rateLimiter.aimd)
	require.InDelta(t, 0.8, p.rateLimiter.aimd.backoff, 1e-9)
	require.InDelta(t, 10.0, p.rateLimiter.aimd.minRate, 1e-9)
	require.InDelta(t, 150.0, p.rateLimiter.aimd.maxRate, 1e-9)
	require.Equal(t, int64(500*time.Millisecond), p.rateLimiter.aimd.interval)
}

func TestBuildOptionsAIMDBadInterval(t *testing.T) {
	t.Parallel()

	cfg := PolicyConfig{
		RateLimit: f64Ptr(100),
		AIMD:      &AIMDConfig{Interval: strPtr("not-a-duration")},
	}

	_, err := BuildOptions(&cfg)
	require.Error(t, err)
	require.ErrorContains(t, err, "aimd.interval") // diagnostic prefix survives
}

func TestReconfigureAIMDViaPolicy(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("aimd-reload",
		WithRateLimit(100, AIMD(AIMDBackoff(0.5), AIMDInterval(time.Second))))

	require.NoError(t, p.Reconfigure(PolicyConfig{
		AIMD: &AIMDConfig{Backoff: f64Ptr(0.9)},
	}))
	require.InDelta(t, 0.9, p.rateLimiter.aimd.backoff, 1e-9)
}

func TestReconfigureAIMDBadInterval(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("aimd-reload-bad",
		WithRateLimit(100, AIMD(AIMDInterval(time.Second))))

	err := p.Reconfigure(PolicyConfig{AIMD: &AIMDConfig{Interval: strPtr("bad")}})
	require.Error(t, err)
	require.ErrorContains(t, err, "aimd.interval") // parse cause prefix
	require.ErrorContains(t, err, "reconfigure")   // double-wrap breadcrumb
}

func TestReconfigureAIMDWithoutRateLimiter(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("no-rl", WithTimeout(time.Second))

	err := p.Reconfigure(PolicyConfig{AIMD: &AIMDConfig{Backoff: f64Ptr(0.9)}})
	// A policy with no rate limiter at all is pattern-absent — distinct from a
	// rate limiter present but built without AIMD (ErrAIMDWithoutRateLimit).
	require.ErrorIs(t, err, ErrPatternAbsent)
	require.ErrorContains(t, err, "rate_limit")
}

func TestReconfigureAIMDWithoutAIMDEnabled(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("rl-no-aimd", WithRateLimit(100))

	err := p.Reconfigure(PolicyConfig{AIMD: &AIMDConfig{Backoff: f64Ptr(0.9)}})
	require.ErrorIs(t, err, ErrAIMDWithoutRateLimit)
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
