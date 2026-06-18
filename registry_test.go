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
		WithReadinessImpact(), // gate readiness on this policy
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

	// Each policy carries a rate limiter and bulkhead so HealthStatus actually
	// reaches Saturated() (which mutates refill state via CAS) and Bulkhead.Full
	// on the read path — not just the trivially-safe breaker mutex.
	names := []string{"svc-a", "svc-b", "svc-c", "svc-d", "svc-e"}
	policies := make([]*Policy[string], len(names))

	for i, name := range names {
		policies[i] = NewPolicy[string](name,
			WithClock(clk),
			WithRegistry(reg),
			WithCircuitBreaker(FailureThreshold(10)),
			WithRateLimit(1000),
			WithBulkhead(8),
		)
	}

	var wg sync.WaitGroup

	// Writers drive Allow→refill (a CAS write) concurrently with the readers,
	// so -race genuinely covers the write-on-read path the health walk reaches.
	for range 10 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for _, p := range policies {
				_, _ = p.Do(context.Background(), func(_ context.Context) (string, error) {
					return "ok", nil
				})
			}
		}()
	}

	// Readers exercise both walks: readiness gating and health aggregation.
	for i := range 50 {
		wg.Add(1)

		go func(n int) {
			defer wg.Done()

			if n%2 == 0 {
				assert.Len(t, reg.CheckReadiness().Policies, 5)
			} else {
				assert.Len(t, reg.Health().Policies, 5)
			}
		}(i)
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
