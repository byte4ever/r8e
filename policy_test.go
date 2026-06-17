package r8e

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test helpers: policyClock — a richer fake clock for policy tests
// ---------------------------------------------------------------------------

// policyClock is a deterministic clock for policy tests. It supports
// controllable Now/Since values and creates timers that fire immediately
// so backoff sleeps don't block.
type policyClock struct {
	mu     sync.Mutex
	now    time.Time
	offset time.Duration
	timers []*policyTimer
}

func newPolicyClock() *policyClock {
	return &policyClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *policyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now.Add(c.offset)
}

func (c *policyClock) Since(t time.Time) time.Duration {
	return c.Now().Sub(t)
}

func (c *policyClock) NewTimer(d time.Duration) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	pt := &policyTimer{ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, pt)
	// Fire immediately for retry/backoff sleeps.
	pt.ch <- c.now.Add(c.offset)
	return pt
}

func (c *policyClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.offset += d
}

type policyTimer struct {
	ch      chan time.Time
	stopped bool
}

func (t *policyTimer) C() <-chan time.Time { return t.ch }

func (t *policyTimer) Stop() bool               { t.stopped = true; return true }
func (t *policyTimer) Reset(time.Duration) bool { return false }

// ---------------------------------------------------------------------------
// TestNewPolicyDefaultClock — NewPolicy with no clock builds and runs with the
// default (real) clock. Fake-clock injection is verified behaviourally in
// timeout_test.go.
// ---------------------------------------------------------------------------

func TestNewPolicyDefaultClock(t *testing.T) {
	p := NewPolicy[string]("test", WithTimeout(time.Second))

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

// ---------------------------------------------------------------------------
// TestPolicyDoPassthrough — Policy with no patterns passes through to fn
// ---------------------------------------------------------------------------

func TestPolicyDoPassthrough(t *testing.T) {
	p := NewPolicy[string]("passthrough")

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "hello", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "hello", result)
}

// ---------------------------------------------------------------------------
// TestPolicyWithTimeout — timeout fires, returns ErrTimeout
// ---------------------------------------------------------------------------

func TestPolicyWithTimeout(t *testing.T) {
	p := NewPolicy[string]("timeout-test",
		WithTimeout(50*time.Millisecond),
	)

	_, err := p.Do(
		context.Background(),
		func(ctx context.Context) (string, error) {
			// Block until context is cancelled (timeout).
			<-ctx.Done()
			return "", ctx.Err()
		},
	)

	require.ErrorIs(t, err, ErrTimeout)
}

// ---------------------------------------------------------------------------
// TestPolicyWithRetry — retries on transient errors, stops on success
// ---------------------------------------------------------------------------

func TestPolicyWithRetry(t *testing.T) {
	clk := newPolicyClock()
	attempt := 0

	p := NewPolicy[string]("retry-test",
		WithClock(clk),
		WithRetry(3, ConstantBackoff(10*time.Millisecond)),
	)

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				return "", errors.New("transient")
			}
			return "recovered", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "recovered", result)
	require.Equal(t, 3, attempt)
}

// ---------------------------------------------------------------------------
// TestPolicyWithCircuitBreaker — circuit breaker opens after failures
// ---------------------------------------------------------------------------

func TestPolicyWithCircuitBreaker(t *testing.T) {
	clk := newPolicyClock()

	p := NewPolicy[string]("cb-test",
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
	)

	// Cause 2 failures to open the circuit.
	for range 2 {
		_, _ = p.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				return "", errors.New("fail")
			},
		)
	}

	// Next call should be rejected by the circuit breaker.
	_, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			t.Fatal("fn should not be called when circuit is open")
			return "unreachable", nil
		},
	)

	require.ErrorIs(t, err, ErrCircuitOpen)
}

// ---------------------------------------------------------------------------
// TestPolicyWithRateLimit — rate limiter rejects when exhausted
// ---------------------------------------------------------------------------

func TestPolicyWithRateLimit(t *testing.T) {
	clk := newPolicyClock()

	// Allow 1 token per second, starting with 1 token in the bucket.
	p := NewPolicy[string]("rl-test",
		WithClock(clk),
		WithRateLimit(1),
	)

	// First call should succeed (consumes the 1 token).
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)

	// Second call should be rate limited (no tokens left).
	_, err = p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			t.Fatal("fn should not be called when rate limited")
			return "unreachable", nil
		},
	)

	require.ErrorIs(t, err, ErrRateLimited)
}

// ---------------------------------------------------------------------------
// TestPolicyWithBulkhead — bulkhead rejects when full
// ---------------------------------------------------------------------------

func TestPolicyWithBulkhead(t *testing.T) {
	p := NewPolicy[string]("bh-test",
		WithBulkhead(1),
	)

	// Acquire the single slot with a blocking call.
	started := make(chan struct{})
	done := make(chan struct{})

	go func() {
		_, _ = p.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				close(started)
				<-done // Block until we release.
				return "first", nil
			},
		)
	}()

	<-started // Wait for goroutine to acquire the slot.

	// Second call should be rejected.
	_, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			t.Fatal("fn should not be called when bulkhead is full")
			return "unreachable", nil
		},
	)

	close(done) // Release the slot.

	require.ErrorIs(t, err, ErrBulkheadFull)
}

// ---------------------------------------------------------------------------
// TestPolicyWithHedge — hedge fires after delay
// ---------------------------------------------------------------------------

func TestPolicyWithHedge(t *testing.T) {
	// RealClock + real durations, made deterministic and instant via synctest.
	synctest.Test(t, func(t *testing.T) {
		var hedgeTriggered atomic.Bool
		hooks := Hooks{
			OnHedgeTriggered: func() { hedgeTriggered.Store(true) },
		}

		p := NewPolicy[string]("hedge-test",
			WithHooks(&hooks),
			WithHedge(10*time.Millisecond),
		)

		result, err := p.Do(
			context.Background(),
			func(ctx context.Context) (string, error) {
				// Slow primary that runs longer than the hedge delay; honours
				// cancellation so the losing call exits promptly.
				select {
				case <-time.After(100 * time.Millisecond):
					return "done", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
		)
		require.NoError(t, err)
		require.Equal(t, "done", result)
		require.True(t, hedgeTriggered.Load())
	})
}

// ---------------------------------------------------------------------------
// TestPolicyWithFallback — returns fallback value on error
// ---------------------------------------------------------------------------

func TestPolicyWithFallback(t *testing.T) {
	p := NewPolicy[string]("fb-test",
		WithFallback("default-user"),
	)

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("service down")
		},
	)
	require.NoError(t, err)
	require.Equal(t, "default-user", result)
}

// ---------------------------------------------------------------------------
// TestPolicyWithFallbackFunc — calls fallback function on error
// ---------------------------------------------------------------------------

func TestPolicyWithFallbackFunc(t *testing.T) {
	p := NewPolicy[string]("fbfn-test",
		WithFallbackFunc(func(err error) (string, error) {
			return "fallback-from-func:" + err.Error(), nil
		}),
	)

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("down")
		},
	)
	require.NoError(t, err)
	require.Equal(t, "fallback-from-func:down", result)
}

// ---------------------------------------------------------------------------
// TestPolicyMultiplePatterns — combines timeout + circuit breaker + retry
// ---------------------------------------------------------------------------

func TestPolicyMultiplePatterns(t *testing.T) {
	clk := newPolicyClock()
	attempt := 0

	p := NewPolicy[string]("multi-test",
		WithClock(clk),
		WithRetry(3, ConstantBackoff(10*time.Millisecond)),
		WithCircuitBreaker(FailureThreshold(10), RecoveryTimeout(time.Hour)),
	)

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				return "", errors.New("transient")
			}
			return "success", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "success", result)
	require.Equal(t, 3, attempt)
}

// ---------------------------------------------------------------------------
// TestPolicyAutoOrdering — patterns execute in correct order regardless of
// option order: retry (innermost) wraps the fn, circuit breaker wraps retry
// ---------------------------------------------------------------------------

func TestPolicyAutoOrdering(t *testing.T) {
	clk := newPolicyClock()

	// Provide options in "wrong" order (retry before circuit breaker).
	// The auto-ordering should put circuit breaker (priority 3) outside retry
	// (priority 6).
	p := NewPolicy[string]("order-test",
		WithClock(clk),
		WithRetry(2, ConstantBackoff(10*time.Millisecond)),
		WithCircuitBreaker(FailureThreshold(3), RecoveryTimeout(time.Hour)),
		WithFallback("fallback-val"),
	)

	// Let all retries fail. The circuit breaker should see failures from retry.
	// After enough policy.Do calls that exhaust retries, the circuit breaker
	// accumulates failure records.
	for range 3 {
		_, _ = p.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				return "", errors.New("always fail")
			},
		)
	}

	// The fallback should catch the ErrCircuitOpen because fallback is
	// outermost.
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			t.Fatal("fn should not be called when circuit is open")
			return "unreachable", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "fallback-val", result)
}

// ---------------------------------------------------------------------------
// TestPolicyHooksWired — hooks fire through policy
// ---------------------------------------------------------------------------

func TestPolicyHooksWired(t *testing.T) {
	clk := newPolicyClock()

	var fallbackUsed atomic.Bool
	hooks := Hooks{
		OnFallbackUsed: func(_ error) { fallbackUsed.Store(true) },
	}

	p := NewPolicy[string]("hooks-test",
		WithClock(clk),
		WithHooks(&hooks),
		WithFallback("safe"),
	)

	_, _ = p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("fail")
	})

	require.True(t, fallbackUsed.Load())
}

// ---------------------------------------------------------------------------
// TestPolicyWithClock — custom clock is used
// ---------------------------------------------------------------------------

func TestPolicyWithClock(t *testing.T) {
	clk := newPolicyClock()

	p := NewPolicy[string]("clock-test",
		WithClock(clk),
		WithTimeout(time.Second),
	)

	// The injected clock must be used by the patterns; a fast call completes.
	// Fake-clock-driven timeout firing is covered in timeout_test.go.
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

// ---------------------------------------------------------------------------
// TestPolicyName — Name() returns correct name
// ---------------------------------------------------------------------------

func TestPolicyName(t *testing.T) {
	p := NewPolicy[string]("my-policy")

	require.Equal(t, "my-policy", p.Name())
}

// ---------------------------------------------------------------------------
// TestPolicyDoConcurrent — concurrent Do calls are safe (for race detector)
// ---------------------------------------------------------------------------

func TestPolicyDoConcurrent(t *testing.T) {
	clk := newPolicyClock()

	p := NewPolicy[int]("concurrent-test",
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(100)),
		WithBulkhead(10),
	)

	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = p.Do(
				context.Background(),
				func(_ context.Context) (int, error) {
					return n, nil
				},
			)
		}(i)
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// TestPolicyPassthroughError — error from fn propagates when no patterns
// ---------------------------------------------------------------------------

func TestPolicyPassthroughError(t *testing.T) {
	p := NewPolicy[string]("error-test")

	sentinel := errors.New("something went wrong")
	_, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", sentinel
		},
	)

	require.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// BenchmarkPolicyDo — benchmark Policy.Do with a single pattern
// ---------------------------------------------------------------------------

func BenchmarkPolicyDo(b *testing.B) {
	p := NewPolicy[string]("bench",
		WithFallback("fallback"),
	)

	ctx := context.Background()

	for b.Loop() {
		_, _ = p.Do(ctx, func(_ context.Context) (string, error) {
			return "ok", nil
		})
	}
}
