package r8e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers: fake clock and timer for deterministic retry testing
// ---------------------------------------------------------------------------

// testTimer is a controllable timer for testing backoff sleeps.
type testTimer struct {
	ch      chan time.Time
	stopped bool
	mu      sync.Mutex
}

func newTestTimer() *testTimer {
	return &testTimer{ch: make(chan time.Time, 1)}
}

func (t *testTimer) C() <-chan time.Time { return t.ch }
func (t *testTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	was := !t.stopped
	t.stopped = true
	return was
}
func (t *testTimer) Reset(time.Duration) bool { return false }

func (t *testTimer) fire() {
	t.ch <- time.Now()
}

// testClock records timer durations and returns controllable timers.
type testClock struct {
	mu        sync.Mutex
	timers    []*testTimer
	durations []time.Duration
}

func newTestClock() *testClock {
	return &testClock{}
}

func (c *testClock) Now() time.Time                  { return time.Now() }
func (c *testClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (c *testClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := newTestTimer()
	c.timers = append(c.timers, t)
	c.durations = append(c.durations, d)
	return t
}

func (c *testClock) getTimer(i int) *testTimer {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.timers[i]
}

func (c *testClock) getDuration(i int) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.durations[i]
}

func (c *testClock) timerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

// immediateTestClock fires timers immediately, useful for simple retry tests.
type immediateTestClock struct {
	mu        sync.Mutex
	durations []time.Duration
}

func newImmediateTestClock() *immediateTestClock {
	return &immediateTestClock{}
}

func (c *immediateTestClock) Now() time.Time { return time.Now() }

func (c *immediateTestClock) Since(
	t time.Time,
) time.Duration {
	return time.Since(t)
}

func (c *immediateTestClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	c.durations = append(c.durations, d)
	c.mu.Unlock()
	t := newTestTimer()
	t.fire() // fire immediately
	return t
}

func (c *immediateTestClock) getDurations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]time.Duration, len(c.durations))
	copy(result, c.durations)
	return result
}

// ---------------------------------------------------------------------------
// Tests: Success on first attempt (no retries)
// ---------------------------------------------------------------------------

func TestDoRetrySuccessOnFirstAttempt(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}

	result, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(100 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
	// No timers should have been created (no backoff sleep needed).
	require.Empty(t, clk.getDurations())
}

// ---------------------------------------------------------------------------
// Tests: Success on Nth attempt after transient failures
// ---------------------------------------------------------------------------

func TestDoRetrySuccessOnThirdAttempt(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	result, err := DoRetry[int](
		context.Background(),
		func(_ context.Context) (int, error) {
			attempt++
			if attempt < 3 {
				return 0, Transient(errors.New("not ready"))
			}
			return 42, nil
		},
		RetryParams{
			MaxAttempts: 5,
			Strategy:    ConstantBackoff(100 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)
	require.NoError(t, err)
	require.Equal(t, 42, result)
	require.Equal(t, 3, attempt)
}

// ---------------------------------------------------------------------------
// Tests: Permanent error stops retries immediately
// ---------------------------------------------------------------------------

func TestDoRetryPermanentErrorStopsImmediately(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			return "", Permanent(errors.New("bad request"))
		},
		RetryParams{
			MaxAttempts: 5,
			Strategy:    ConstantBackoff(100 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)

	require.Error(t, err)
	require.Equal(t, 1, attempt)
	// Should NOT wrap with ErrRetriesExhausted.
	require.NotErrorIs(t, err, ErrRetriesExhausted)
	require.True(t, IsPermanent(err), "expected permanent error to be detectable")
}

// ---------------------------------------------------------------------------
// Tests: All retries exhausted returns ErrRetriesExhausted
// ---------------------------------------------------------------------------

func TestDoRetryAllRetriesExhausted(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			return "", Transient(errors.New("still failing"))
		},
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(100 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)

	require.Error(t, err)
	require.ErrorIs(t, err, ErrRetriesExhausted)
	require.Equal(t, 3, attempt)
	// The last error should be unwrappable.
	require.ErrorIs(t, err, ErrRetriesExhausted)
}

// ---------------------------------------------------------------------------
// Tests: MaxDelay caps the backoff
// ---------------------------------------------------------------------------

func TestDoRetryMaxDelayCapsBackoff(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	_, _ = DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			return "", Transient(errors.New("fail"))
		},
		RetryParams{
			MaxAttempts: 4,
			Strategy: ExponentialBackoff(
				100 * time.Millisecond,
			), // delays: 100ms, 200ms, 400ms
			Hooks: hooks,
			Clock: clk,
			Opts:  []RetryOption{MaxDelay(150 * time.Millisecond)},
		},
	)

	durations := clk.getDurations()
	for i, d := range durations {
		require.LessOrEqualf(t, d, 150*time.Millisecond, "timer %d: duration = %v, want <= 150ms", i, d)
	}
	// First delay is 100ms (under cap), second would be 200ms capped to 150ms.
	if len(durations) >= 1 {
		require.Equal(t, 100*time.Millisecond, durations[0])
	}
	if len(durations) >= 2 {
		require.Equal(t, 150*time.Millisecond, durations[1])
	}
}

// ---------------------------------------------------------------------------
// Tests: PerAttemptTimeout cancels slow individual attempts
// ---------------------------------------------------------------------------

func TestDoRetryPerAttemptTimeout(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	result, err := DoRetry[string](
		context.Background(),
		func(ctx context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				// Simulate a slow operation that will be cancelled by
				// per-attempt timeout.
				<-ctx.Done()
				return "", ctx.Err()
			}
			return "done", nil
		},
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(1 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
			Opts: []RetryOption{
				PerAttemptTimeout(10 * time.Millisecond),
			},
		},
	)
	require.NoError(t, err)
	require.Equal(t, "done", result)
	require.Equal(t, 3, attempt)
}

// ---------------------------------------------------------------------------
// Tests: RetryIf predicate controls retryability
// ---------------------------------------------------------------------------

func TestDoRetryRetryIfPredicateStopsRetry(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			return "", errors.New("custom non-retryable")
		},
		RetryParams{
			MaxAttempts: 5,
			Strategy:    ConstantBackoff(1 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
			Opts: []RetryOption{RetryIf(func(err error) bool {
				return false // never retry
			})},
		},
	)

	require.Error(t, err)
	require.Equalf(t, 1, attempt, "expected 1 attempt when RetryIf returns false, got %d", attempt)
	// Should NOT wrap with ErrRetriesExhausted.
	require.NotErrorIs(t, err, ErrRetriesExhausted)
}

func TestDoRetryRetryIfPredicateAllowsRetry(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	result, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				return "", errors.New("retryable by predicate")
			}
			return "success", nil
		},
		RetryParams{
			MaxAttempts: 5,
			Strategy:    ConstantBackoff(1 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
			Opts: []RetryOption{RetryIf(func(err error) bool {
				return true // always retry
			})},
		},
	)
	require.NoError(t, err)
	require.Equal(t, "success", result)
	require.Equal(t, 3, attempt)
}

// ---------------------------------------------------------------------------
// Tests: Context cancellation during backoff sleep
// ---------------------------------------------------------------------------

func TestDoRetryContextCancellationDuringSleep(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	hooks := &Hooks{}
	attempt := 0

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	var retErr error

	go func() {
		_, retErr = DoRetry[string](
			ctx,
			func(_ context.Context) (string, error) {
				attempt++
				return "", Transient(errors.New("fail"))
			},
			RetryParams{
				MaxAttempts: 5,
				Strategy:    ConstantBackoff(time.Hour), // very long backoff
				Hooks:       hooks,
				Clock:       clk,
			},
		)
		close(done)
	}()

	// Wait for the first timer to be created (the backoff sleep).
	for {
		if clk.timerCount() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	// Cancel the context during the backoff sleep.
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DoRetry did not return after context cancellation")
	}

	require.Error(t, retErr)
	require.ErrorIs(t, retErr, context.Canceled)
}

// ---------------------------------------------------------------------------
// Tests: Zero/one maxAttempts executes exactly once
// ---------------------------------------------------------------------------

func TestDoRetryZeroMaxAttemptsExecutesOnce(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			return "", Transient(errors.New("fail"))
		},
		RetryParams{
			MaxAttempts: 0,
			Strategy:    ConstantBackoff(100 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)

	require.Equalf(t, 1, attempt, "expected 1 attempt with maxAttempts=0, got %d", attempt)
	// With only one attempt and failure, no retries => should wrap with
	// ErrRetriesExhausted.
	require.ErrorIs(t, err, ErrRetriesExhausted)
}

func TestDoRetryOneMaxAttemptsExecutesOnce(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			return "", Transient(errors.New("fail"))
		},
		RetryParams{
			MaxAttempts: 1,
			Strategy:    ConstantBackoff(100 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)

	require.Equalf(t, 1, attempt, "expected 1 attempt with maxAttempts=1, got %d", attempt)
	require.ErrorIs(t, err, ErrRetriesExhausted)
}

func TestDoRetryZeroMaxAttemptsSucceeds(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}

	result, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		RetryParams{
			MaxAttempts: 0,
			Strategy:    ConstantBackoff(100 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

// ---------------------------------------------------------------------------
// Tests: OnRetry hook is called with correct attempt number and error
// ---------------------------------------------------------------------------

func TestDoRetryOnRetryHookCalledWithCorrectArgs(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	var hookCalls []struct {
		attempt int
		err     error
	}
	hooks := &Hooks{
		OnRetry: func(attempt int, err error) {
			hookCalls = append(hookCalls, struct {
				attempt int
				err     error
			}{attempt, err})
		},
	}
	attempt := 0

	_, _ = DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			return "", Transient(errors.New("fail"))
		},
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(1 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)

	// 3 attempts, but OnRetry should be called for attempts 1 and 2 (not after
	// the last). Wait: the spec says "emit hooks.emitRetry(attempt, err) after
	// each failed attempt (before sleeping)"
	// For 3 attempts, we have failures on attempt 0, 1, 2.
	// After attempt 0 and 1, we retry (emit hook). After attempt 2, we're
	// exhausted (no hook needed, but per spec it says "after each failed
	// attempt before sleeping" - on last attempt there's no sleep). Actually,
	// let me re-read: hooks are emitted after each failed attempt before
	// sleeping. Attempt 0 fails -> emit hook(1, err) -> sleep -> attempt 1
	// fails -> emit hook(2, err) -> sleep -> attempt 2 fails -> exhausted.
	// So 2 hook calls for 3 attempts.
	require.Len(t, hookCalls, 2)
	// Hooks receive 1-indexed attempt numbers.
	require.Equal(t, 1, hookCalls[0].attempt)
	require.Equal(t, 2, hookCalls[1].attempt)
}

// ---------------------------------------------------------------------------
// Tests: Unclassified errors are treated as transient (retried)
// ---------------------------------------------------------------------------

func TestDoRetryUnclassifiedErrorsAreRetried(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	attempt := 0

	result, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				return "", errors.New("plain error, not classified")
			}
			return "recovered", nil
		},
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(1 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)
	require.NoError(t, err)
	require.Equal(t, "recovered", result)
	require.Equal(t, 3, attempt)
}

// ---------------------------------------------------------------------------
// Tests: RetryOption constructors
// ---------------------------------------------------------------------------

func TestMaxDelayOption(t *testing.T) {
	t.Parallel()
	var cfg retryConfig
	MaxDelay(500 * time.Millisecond)(&cfg)
	require.Equal(t, 500*time.Millisecond, cfg.maxDelay)
}

func TestPerAttemptTimeoutOption(t *testing.T) {
	t.Parallel()
	var cfg retryConfig
	PerAttemptTimeout(2 * time.Second)(&cfg)
	require.Equal(t, 2*time.Second, cfg.perAttemptTimeout)
}

func TestRetryIfOption(t *testing.T) {
	t.Parallel()
	var cfg retryConfig
	fn := func(err error) bool { return true }
	RetryIf(fn)(&cfg)
	require.NotNil(t, cfg.retryIf)
}

// ---------------------------------------------------------------------------
// Tests: Error wrapping structure
// ---------------------------------------------------------------------------

func TestDoRetryExhaustedErrorWrapsLastError(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()
	hooks := &Hooks{}

	sentinel := errors.New("the last error")
	attempt := 0

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt == 1 {
				return "", Transient(errors.New("first error"))
			}
			return "", Transient(sentinel)
		},
		RetryParams{
			MaxAttempts: 2,
			Strategy:    ConstantBackoff(1 * time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
		},
	)

	require.ErrorIs(t, err, ErrRetriesExhausted)
	// The sentinel (wrapped in transient) should be findable.
	require.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// Tests: Timer is stopped on context cancellation
// ---------------------------------------------------------------------------

func TestDoRetryTimerStoppedOnCancel(t *testing.T) {
	t.Parallel()
	clk := newTestClock()
	hooks := &Hooks{}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		_, _ = DoRetry[string](
			ctx,
			func(_ context.Context) (string, error) {
				return "", Transient(errors.New("fail"))
			},
			RetryParams{
				MaxAttempts: 5,
				Strategy:    ConstantBackoff(time.Hour),
				Hooks:       hooks,
				Clock:       clk,
			},
		)
		close(done)
	}()

	// Wait for timer creation.
	for {
		if clk.timerCount() > 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("DoRetry did not return after context cancellation")
	}

	// The timer should have been stopped.
	timer := clk.getTimer(0)
	timer.mu.Lock()
	stopped := timer.stopped
	timer.mu.Unlock()
	require.True(t, stopped, "expected timer to be stopped on context cancellation")
}

// ---------------------------------------------------------------------------
// Tests: Backoff strategy receives correct 0-indexed attempts
// ---------------------------------------------------------------------------

func TestDoRetryBackoffStrategyReceivesCorrectAttempts(t *testing.T) {
	t.Parallel()
	var receivedAttempts []int
	strategy := BackoffFunc(func(attempt int) time.Duration {
		receivedAttempts = append(receivedAttempts, attempt)
		return time.Millisecond
	})

	clk := newImmediateTestClock()
	hooks := &Hooks{}

	_, _ = DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", Transient(errors.New("fail"))
		},
		RetryParams{
			MaxAttempts: 4,
			Strategy:    strategy,
			Hooks:       hooks,
			Clock:       clk,
		},
	)

	// 4 attempts, backoff called between attempts (3 times: after attempt 0, 1,
	// 2).
	want := []int{0, 1, 2}
	assert.Equal(t, want, receivedAttempts)
}

// ---------------------------------------------------------------------------
// Tests: Nil hooks do not panic
// ---------------------------------------------------------------------------

func TestDoRetryNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()
	clk := newImmediateTestClock()

	_, _ = DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", Transient(errors.New("fail"))
		},
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(1 * time.Millisecond),
			Hooks:       nil, // nil *Hooks must be a no-op
			Clock:       clk,
		},
	)
	// If we get here without panicking, the test passes.
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkRetry(b *testing.B) {
	clk := newImmediateTestClock()
	hooks := &Hooks{}
	ctx := context.Background()
	strategy := ConstantBackoff(time.Millisecond)

	for b.Loop() {
		_, _ = DoRetry[string](
			ctx,
			func(_ context.Context) (string, error) {
				return "ok", nil
			},
			RetryParams{
				MaxAttempts: 3,
				Strategy:    strategy,
				Hooks:       hooks,
				Clock:       clk,
			},
		)
	}
}
