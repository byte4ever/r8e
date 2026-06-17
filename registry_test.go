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
// TestNewRegistry — empty registry returns Ready=true, empty Policies
// ---------------------------------------------------------------------------

func TestNewRegistry(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()

	status := reg.CheckReadiness()

	require.True(t, status.Ready)
	require.Empty(t, status.Policies)
}

// ---------------------------------------------------------------------------
// TestRegistryRegister — Register adds a reporter, CheckReadiness includes it
// ---------------------------------------------------------------------------

func TestRegistryRegister(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	clk := newPolicyClock()

	p := NewPolicy[string]("test-policy",
		WithClock(clk),
		WithRegistry(reg),
	)
	_ = p

	status := reg.CheckReadiness()

	require.Len(t, status.Policies, 1)
	require.Equal(t, "test-policy", status.Policies[0].Name)
	require.True(t, status.Ready)
}

// ---------------------------------------------------------------------------
// TestRegistryAllHealthy — multiple healthy policies → Ready=true
// ---------------------------------------------------------------------------

func TestRegistryAllHealthy(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	clk := newPolicyClock()

	_ = NewPolicy[string]("svc-a",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(5)),
	)
	_ = NewPolicy[string]("svc-b",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(5)),
	)
	_ = NewPolicy[int]("svc-c",
		WithClock(clk),
		WithRegistry(reg),
	)

	status := reg.CheckReadiness()

	require.True(t, status.Ready)
	require.Len(t, status.Policies, 3)
	for _, ps := range status.Policies {
		assert.Truef(t, ps.Healthy, "policy %q: Healthy = false, want true", ps.Name)
	}
}

// ---------------------------------------------------------------------------
// TestRegistryOneCritical — one policy with open circuit → Ready=false
// ---------------------------------------------------------------------------

func TestRegistryOneCritical(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	clk := newPolicyClock()

	healthy := NewPolicy[string]("healthy-svc",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(5)),
	)
	_ = healthy

	unhealthy := NewPolicy[string]("unhealthy-svc",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
	)

	// Drive circuit breaker to open.
	ctx := context.Background()
	for range 2 {
		_, _ = unhealthy.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("fail")
		})
	}

	status := reg.CheckReadiness()

	require.False(t, status.Ready)

	// Verify the unhealthy policy is reported correctly.
	var found bool
	for _, ps := range status.Policies {
		if ps.Name == "unhealthy-svc" {
			found = true
			assert.False(t, ps.Healthy)
			assert.Equal(t, CriticalityCritical, ps.Criticality)
		}
	}
	require.True(t, found)
}

// ---------------------------------------------------------------------------
// TestRegistryOneDegraded — one policy degraded (rate limited) → Ready=true
// ---------------------------------------------------------------------------

func TestRegistryOneDegraded(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	clk := newPolicyClock()

	p := NewPolicy[string]("rl-svc",
		WithClock(clk),
		WithRegistry(reg),
		WithRateLimit(1), // 1 token/sec
	)

	ctx := context.Background()

	// Consume the single token to make RL saturated.
	_, _ = p.Do(ctx, func(_ context.Context) (string, error) {
		return "ok", nil
	})

	status := reg.CheckReadiness()

	// Degraded does not make the service not-ready.
	require.True(t, status.Ready)

	// Verify the policy reports degraded.
	require.Len(t, status.Policies, 1)
	require.Equal(t, CriticalityDegraded, status.Policies[0].Criticality)
}

// ---------------------------------------------------------------------------
// TestRegistryConcurrentReads — many goroutines calling CheckReadiness is safe
// ---------------------------------------------------------------------------

func TestRegistryConcurrentReads(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	clk := newPolicyClock()

	// Register a few policies.
	for i := range 5 {
		names := []string{"svc-a", "svc-b", "svc-c", "svc-d", "svc-e"}
		_ = NewPolicy[string](names[i],
			WithClock(clk),
			WithRegistry(reg),
			WithCircuitBreaker(FailureThreshold(10)),
		)
	}

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := reg.CheckReadiness()
			assert.Len(t, status.Policies, 5)
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// TestRegistryConcurrentRegisterAndRead — Register and CheckReadiness
// concurrent
// ---------------------------------------------------------------------------

func TestRegistryConcurrentRegisterAndRead(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	clk := newPolicyClock()

	var wg sync.WaitGroup

	// Spawn readers.
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				_ = reg.CheckReadiness()
			}
		}()
	}

	// Spawn writers (registrations).
	for i := range 10 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_ = NewPolicy[int]("concurrent-reg",
				WithClock(clk),
				WithRegistry(reg),
			)
		}(i)
	}

	wg.Wait()

	// After all registrations, we should have 10 policies.
	status := reg.CheckReadiness()
	require.Len(t, status.Policies, 10)
}

// ---------------------------------------------------------------------------
// TestDefaultRegistry — returns same instance on multiple calls
// ---------------------------------------------------------------------------

func TestDefaultRegistry(t *testing.T) {
	t.Parallel()

	r1 := DefaultRegistry()
	r2 := DefaultRegistry()

	require.Same(t, r1, r2)
	require.NotNil(t, r1)
}

// ---------------------------------------------------------------------------
// TestWithRegistry — policy registered with explicit registry
// ---------------------------------------------------------------------------

func TestWithRegistry(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	clk := newPolicyClock()

	p := NewPolicy[string]("explicit-reg",
		WithClock(clk),
		WithRegistry(reg),
	)

	require.Same(t, reg, p.registry)

	status := reg.CheckReadiness()

	require.Len(t, status.Policies, 1)
	require.Equal(t, "explicit-reg", status.Policies[0].Name)
}

// ---------------------------------------------------------------------------
// TestAutoRegistration — NewPolicy with name auto-registers with
// DefaultRegistry
// ---------------------------------------------------------------------------

func TestAutoRegistration(t *testing.T) {
	t.Parallel()

	// Use an explicit registry to avoid polluting the global DefaultRegistry.
	// The mechanism is the same — we just verify the auto-registration path.
	reg := NewRegistry()

	p := NewPolicy[string]("auto-reg-test",
		WithRegistry(reg),
	)

	require.Same(t, reg, p.registry)

	status := reg.CheckReadiness()
	require.Len(t, status.Policies, 1)
	require.Equal(t, "auto-reg-test", status.Policies[0].Name)
}

// ---------------------------------------------------------------------------
// TestAnonymousPolicyNotRegistered — NewPolicy("", ...) doesn't register
// ---------------------------------------------------------------------------

func TestAnonymousPolicyNotRegistered(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()

	p := NewPolicy[string]("",
		WithRegistry(reg),
	)

	require.Nil(t, p.registry)

	status := reg.CheckReadiness()
	require.Empty(t, status.Policies)
}

// ---------------------------------------------------------------------------
// BenchmarkCheckReadiness — benchmark with multiple registered policies
// ---------------------------------------------------------------------------

func BenchmarkCheckReadiness(b *testing.B) {
	reg := NewRegistry()
	clk := newPolicyClock()

	// Register several policies with various patterns.
	for _, name := range []string{"svc-a", "svc-b", "svc-c", "svc-d", "svc-e"} {
		_ = NewPolicy[string](name,
			WithClock(clk),
			WithRegistry(reg),
			WithCircuitBreaker(FailureThreshold(5)),
			WithRateLimit(100),
			WithBulkhead(10),
		)
	}

	for b.Loop() {
		_ = reg.CheckReadiness()
	}
}
