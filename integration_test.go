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
// TestIntegrationFullChainSuccess — Full policy chain, success on 2nd attempt
// ---------------------------------------------------------------------------

func TestIntegrationFullChainSuccess(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()
	reg := NewRegistry()

	var (
		retryCount atomic.Int32
		bhAcquired atomic.Int32
		bhReleased atomic.Int32
	)

	hooks := Hooks{
		OnRetry:            func(_ int, _ error) { retryCount.Add(1) },
		OnBulkheadAcquired: func() { bhAcquired.Add(1) },
		OnBulkheadReleased: func() { bhReleased.Add(1) },
	}

	p := NewPolicy[string]("full-chain",
		WithClock(clk),
		WithRegistry(reg),
		WithHooks(&hooks),
		WithTimeout(5*time.Second),
		WithCircuitBreaker(FailureThreshold(10), RecoveryTimeout(time.Hour)),
		WithRetry(3, ConstantBackoff(10*time.Millisecond)),
		WithBulkhead(5),
		WithRateLimit(1000),
	)

	attempt := 0
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 2 {
				return "", errors.New("transient failure")
			}
			return "success", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "success", result)
	require.Equal(t, 2, attempt)
	// Retry hook should have fired once (before the 2nd attempt).
	require.Equal(t, int32(1), retryCount.Load())
	// Bulkhead should have been acquired and released.
	require.GreaterOrEqual(t, bhAcquired.Load(), int32(1))
	require.GreaterOrEqual(t, bhReleased.Load(), int32(1))

	// Circuit breaker should still be closed (only 1 failure out of threshold
	// 10).
	status := p.HealthStatus()
	require.True(t, status.Healthy)
}

// ---------------------------------------------------------------------------
// TestIntegrationPresetPolicy — Use preset + override
// ---------------------------------------------------------------------------

func TestIntegrationPresetPolicy(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()
	reg := NewRegistry()

	// Use StandardHTTPClient preset.
	presetOpts := StandardHTTPClient()

	// Append overrides: custom clock, registry, and a fallback.
	opts := append(presetOpts,
		WithClock(clk),
		WithRegistry(reg),
		WithFallback("preset-fallback"),
	)

	p := NewPolicy[string]("preset-policy", opts...)

	// Successful call.
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "from-preset", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "from-preset", result)

	// Verify the fallback override works: fail enough to exhaust retries +
	// trigger circuit breaker, then fallback catches.
	for range 10 {
		_, _ = p.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				return "", errors.New("always fail")
			},
		)
	}

	result, err = p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("still failing")
		},
	)
	require.NoError(t, err)
	require.Equal(t, "preset-fallback", result)
}

// ---------------------------------------------------------------------------
// TestIntegrationDoConvenience — r8e.Do[T] with retry + timeout
// ---------------------------------------------------------------------------

func TestIntegrationDoConvenience(t *testing.T) {
	clk := newPolicyClock()

	attempt := 0
	result, err := Do[string](context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				return "", errors.New("transient")
			}
			return "convenience-ok", nil
		},
		WithClock(clk),
		WithRetry(5, ConstantBackoff(10*time.Millisecond)),
		WithTimeout(10*time.Second),
	)
	require.NoError(t, err)
	require.Equal(t, "convenience-ok", result)
	require.Equal(t, 3, attempt)
}

// ---------------------------------------------------------------------------
// TestIntegrationConcurrentPolicy — 100 goroutines, no races, no panics
// ---------------------------------------------------------------------------

func TestIntegrationConcurrentPolicy(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()
	reg := NewRegistry()

	p := NewPolicy[int]("concurrent-integ",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(200), RecoveryTimeout(time.Hour)),
		WithRetry(2, ConstantBackoff(1*time.Millisecond)),
		WithBulkhead(50),
		WithRateLimit(10000),
	)

	const numGoroutines = 100
	var (
		wg        sync.WaitGroup
		successes atomic.Int64
		failures  atomic.Int64
	)

	wg.Add(numGoroutines)
	for i := range numGoroutines {
		go func(n int) {
			defer wg.Done()
			result, err := p.Do(
				context.Background(),
				func(_ context.Context) (int, error) {
					// Alternate between success and failure to exercise both
					// paths.
					if n%3 == 0 {
						return 0, errors.New("simulated failure")
					}
					return n, nil
				},
			)
			if err != nil {
				failures.Add(1)
			} else {
				_ = result
				successes.Add(1)
			}
		}(i)
	}

	wg.Wait()

	total := successes.Load() + failures.Load()
	require.Equal(t, int64(numGoroutines), total)

	// Some should have succeeded (2/3 of inputs succeed on first try).
	require.NotZero(t, successes.Load())

	t.Logf(
		"concurrent results: %d successes, %d failures",
		successes.Load(),
		failures.Load(),
	)
}

// ---------------------------------------------------------------------------
// TestIntegrationHierarchicalHealth — Parent depends on child, CB opens
// ---------------------------------------------------------------------------

func TestIntegrationHierarchicalHealth(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()
	reg := NewRegistry()

	// Create a child policy with a circuit breaker. It gates readiness so the
	// registry reports not-ready when the child's breaker opens.
	child := NewPolicy[string]("child-db",
		WithClock(clk),
		WithRegistry(reg),
		WithReadinessImpact(),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
	)

	// Create a parent policy that depends on the child.
	parent := NewPolicy[string]("parent-api",
		WithClock(clk),
		WithRegistry(reg),
		DependsOn(child),
	)

	// Initially both should be healthy.
	readiness := reg.CheckReadiness()
	require.True(t, readiness.Ready)

	parentHealth := parent.HealthStatus()
	require.True(t, parentHealth.Healthy)

	// Drive the child's circuit breaker to open.
	for range 2 {
		_, _ = child.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				return "", errors.New("child failure")
			},
		)
	}

	// Verify child is unhealthy.
	childHealth := child.HealthStatus()
	require.False(t, childHealth.Healthy)
	require.Equal(t, CriticalityCritical, childHealth.Criticality)

	// Verify parent health reports degraded due to dependency.
	parentHealth = parent.HealthStatus()
	require.GreaterOrEqual(t, parentHealth.Criticality, CriticalityDegraded)
	require.NotEmpty(t, parentHealth.Dependencies)
	depHealth := parentHealth.Dependencies[0]
	require.False(t, depHealth.Healthy)

	// Verify registry-level readiness reports not ready (child is
	// critical+unhealthy).
	readiness = reg.CheckReadiness()
	require.False(t, readiness.Ready)
}

// ---------------------------------------------------------------------------
// TestIntegrationPermanentErrorStopsRetry — Permanent error stops retry
// ---------------------------------------------------------------------------

func TestIntegrationPermanentErrorStopsRetry(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()
	reg := NewRegistry()

	p := NewPolicy[string]("perm-stop",
		WithClock(clk),
		WithRegistry(reg),
		WithRetry(5, ConstantBackoff(10*time.Millisecond)),
	)

	attempt := 0
	sentinel := errors.New("fatal database error")
	_, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			// Return a permanent error on the first attempt.
			return "", Permanent(sentinel)
		},
	)

	// Should have been called exactly once — permanent stops retry immediately.
	require.Equal(t, 1, attempt)

	// The error should be the permanent-wrapped sentinel.
	require.True(t, IsPermanent(err))
	require.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// TestIntegrationFallbackCatchesAllErrors — Fallback catches retries + CB
// ---------------------------------------------------------------------------

func TestIntegrationFallbackCatchesAllErrors(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()
	reg := NewRegistry()

	var fallbackErrors []error
	var mu sync.Mutex

	p := NewPolicy[string]("fallback-all",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(3), RecoveryTimeout(time.Hour)),
		WithRetry(2, ConstantBackoff(10*time.Millisecond)),
		WithFallbackFunc[string](func(err error) (string, error) {
			mu.Lock()
			fallbackErrors = append(fallbackErrors, err)
			mu.Unlock()
			return "fallback-value", nil
		}),
	)

	// Exhaust retries several times to drive circuit to open.
	for range 3 {
		result, err := p.Do(
			context.Background(),
			func(_ context.Context) (string, error) {
				return "", errors.New("always fail")
			},
		)
		require.NoError(t, err)
		require.Equal(t, "fallback-value", result)
	}

	// At this point, the circuit breaker should be open (3 failures recorded
	// because retry exhausts 2 attempts per Do, and the CB sees the final
	// failure
	// from the retry layer).
	// The next call should get ErrCircuitOpen caught by fallback.
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			t.Fatal("fn should not be called when circuit is open")
			return "unreachable", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "fallback-value", result)

	// Verify fallback caught different types of errors.
	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(fallbackErrors), 2)

	// At least one should be ErrRetriesExhausted and at least one
	// ErrCircuitOpen.
	var gotRetries, gotCircuit bool
	for _, e := range fallbackErrors {
		if errors.Is(e, ErrRetriesExhausted) {
			gotRetries = true
		}
		if errors.Is(e, ErrCircuitOpen) {
			gotCircuit = true
		}
	}
	assert.True(t, gotRetries, "fallback should have caught ErrRetriesExhausted")
	assert.True(t, gotCircuit, "fallback should have caught ErrCircuitOpen")
}
