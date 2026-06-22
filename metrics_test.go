package r8e

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMetricsRetryAndFallbackCounters drives retries that exhaust into a
// fallback and checks both the counters and that the user hooks still fire.
func TestMetricsRetryAndFallbackCounters(t *testing.T) {
	var retryHook, fallbackHook atomic.Bool

	p := NewPolicy[string]("retry-fb",
		WithRegistry(NewRegistry()),
		WithClock(newPolicyClock()),
		WithRetry(2, ConstantBackoff(time.Millisecond)),
		WithFallbackFunc(func(error) (string, error) { return "fb", nil }),
		WithHooks(&Hooks{
			OnRetry:        func(int, error) { retryHook.Store(true) },
			OnFallbackUsed: func(error) { fallbackHook.Store(true) },
		}),
	)

	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "", errors.New("boom") },
	)
	require.NoError(t, err)
	require.Equal(t, "fb", result)

	metrics := p.Metrics()
	assert.Positive(t, metrics.Retries)
	assert.Equal(t, int64(1), metrics.FallbacksUsed)
	assert.True(t, retryHook.Load(), "user OnRetry should fire")
	assert.True(t, fallbackHook.Load(), "user OnFallbackUsed should fire")
}

// TestMetricsCircuitLifecycle drives open -> half-open -> close and checks the
// counters, the live CircuitState gauge, and the user hooks.
func TestMetricsCircuitLifecycle(t *testing.T) {
	clk := &stubClock{now: time.Now()}

	var opened, halfOpened, closed atomic.Bool

	p := NewPolicy[string]("cb-life",
		WithRegistry(NewRegistry()),
		WithClock(clk),
		WithCircuitBreaker(
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
			HalfOpenMaxAttempts(1),
		),
		WithHooks(&Hooks{
			OnCircuitOpen:     func() { opened.Store(true) },
			OnCircuitHalfOpen: func() { halfOpened.Store(true) },
			OnCircuitClose:    func() { closed.Store(true) },
		}),
	)

	// Failure opens the breaker.
	_, _ = p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("down")
	})
	require.Equal(t, "open", p.Metrics().CircuitState)

	// After recovery, a success probes half-open and closes the breaker.
	clk.setElapsed(2 * time.Second)
	_, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	metrics := p.Metrics()
	assert.Equal(t, "closed", metrics.CircuitState)
	assert.Equal(t, int64(1), metrics.CircuitOpens)
	assert.Equal(t, int64(1), metrics.CircuitHalfOpens)
	assert.Equal(t, int64(1), metrics.CircuitCloses)
	assert.True(t, opened.Load() && halfOpened.Load() && closed.Load())
}

func TestMetricsRampRecovery(t *testing.T) {
	clk := &stubClock{now: time.Now()}

	var ramped atomic.Bool

	p := NewPolicy[string]("cb-ramp",
		WithRegistry(NewRegistry()),
		WithClock(clk),
		WithCircuitBreaker(
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
			HalfOpenMaxAttempts(1),
			RampRecovery(100*time.Second),
		),
		WithHooks(&Hooks{
			OnCircuitRamping: func() { ramped.Store(true) },
		}),
	)

	// A failure trips the breaker.
	_, _ = p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("down")
	})
	require.Equal(t, "open", p.Metrics().CircuitState)

	// After the recovery timeout a successful probe enters the slow-start ramp
	// instead of closing straight to full traffic.
	clk.setElapsed(2 * time.Second)
	_, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	metrics := p.Metrics()
	assert.Equal(t, "ramping", metrics.CircuitState)
	assert.Equal(t, int64(1), metrics.CircuitRamps)
	assert.InDelta(t, 0.1, metrics.RampRecoveryFraction, 1e-9, "floored at initial 0.1 (2s/100s)")
	assert.True(t, ramped.Load())

	// A draw at or above the ramp fraction sheds the call with ErrCircuitRamping.
	p.circuitBreaker.sampler = func() float64 { return 1 }
	_, err = p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})
	require.ErrorIs(t, err, ErrCircuitRamping)
}

func TestMetricsRateLimited(t *testing.T) {
	var hook atomic.Bool

	// rate=1 -> capacity is one token; a non-advancing clock means no refill.
	p := NewPolicy[string]("rl",
		WithRegistry(NewRegistry()),
		WithClock(&stubClock{now: time.Now()}),
		WithRateLimit(1),
		WithHooks(&Hooks{OnRateLimited: func() { hook.Store(true) }}),
	)

	ok := func(_ context.Context) (string, error) { return "ok", nil }
	_, _ = p.Do(context.Background(), ok) // consumes the only token

	_, err := p.Do(context.Background(), ok)
	require.ErrorIs(t, err, ErrRateLimited)

	metrics := p.Metrics()
	assert.GreaterOrEqual(t, metrics.RateLimited, int64(1))
	assert.True(t, metrics.Saturated)
	assert.True(t, hook.Load())
}

func TestMetricsBulkheadRejectedAndGauge(t *testing.T) {
	var hook atomic.Bool

	p := NewPolicy[string]("bh",
		WithRegistry(NewRegistry()),
		WithBulkhead(1),
		WithHooks(&Hooks{OnBulkheadFull: func() { hook.Store(true) }}),
	)

	started := make(chan struct{})
	release := make(chan struct{})

	go func() {
		_, _ = p.Do(context.Background(), func(_ context.Context) (string, error) {
			close(started)
			<-release

			return "held", nil
		})
	}()

	<-started // the only slot is now held

	_, err := p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "rejected", nil
	})
	require.ErrorIs(t, err, ErrBulkheadFull)

	metrics := p.Metrics()
	assert.GreaterOrEqual(t, metrics.BulkheadRejected, int64(1))
	assert.Equal(t, int64(1), metrics.BulkheadCap)
	assert.Equal(t, int64(1), metrics.BulkheadInUse)
	assert.True(t, hook.Load())

	close(release)
}

func TestMetricsTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var hook atomic.Bool

		p := NewPolicy[string]("to",
			WithRegistry(NewRegistry()),
			WithTimeout(10*time.Millisecond),
			WithHooks(&Hooks{OnTimeout: func() { hook.Store(true) }}),
		)

		_, err := p.Do(context.Background(), func(ctx context.Context) (string, error) {
			<-ctx.Done()

			return "", ctx.Err()
		})
		require.ErrorIs(t, err, ErrTimeout)

		assert.Equal(t, int64(1), p.Metrics().Timeouts)
		assert.True(t, hook.Load())
	})
}

func TestMetricsHedge(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var triggered, won atomic.Bool

		p := NewPolicy[string]("hedge",
			WithRegistry(NewRegistry()),
			WithHedge(20*time.Millisecond),
			WithHooks(&Hooks{
				OnHedgeTriggered: func() { triggered.Store(true) },
				OnHedgeWon:       func() { won.Store(true) },
			}),
		)

		var calls atomic.Int32

		result, err := p.Do(context.Background(), func(ctx context.Context) (string, error) {
			if calls.Add(1) == 1 {
				select {
				case <-time.After(5 * time.Second):
					return "primary", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			}

			return "hedge", nil
		})
		require.NoError(t, err)
		require.Equal(t, "hedge", result)

		metrics := p.Metrics()
		assert.Equal(t, int64(1), metrics.HedgesTriggered)
		assert.Equal(t, int64(1), metrics.HedgesWon)
		assert.True(t, triggered.Load() && won.Load())
	})
}

func TestRegistrySnapshot(t *testing.T) {
	reg := NewRegistry()

	_ = NewPolicy[string]("alpha", WithRegistry(reg), WithCircuitBreaker())
	_ = NewPolicy[int]("beta", WithRegistry(reg), WithBulkhead(4))

	snapshot := reg.Snapshot()
	require.Len(t, snapshot, 2)

	byName := make(map[string]PolicyMetrics, len(snapshot))
	for _, m := range snapshot {
		byName[m.Name] = m
	}

	require.Contains(t, byName, "alpha")
	require.Contains(t, byName, "beta")
	assert.Equal(t, "closed", byName["alpha"].CircuitState)
	assert.Equal(t, int64(4), byName["beta"].BulkheadCap)
}

func TestRegistrySnapshotEmpty(t *testing.T) {
	assert.Empty(t, NewRegistry().Snapshot())
}
