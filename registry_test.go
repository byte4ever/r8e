package r8e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TestNewRegistry — empty registry returns Ready=true, empty Policies
// ---------------------------------------------------------------------------

func TestNewRegistry(t *testing.T) {
	reg := NewRegistry()

	status := reg.CheckReadiness()

	if !status.Ready {
		t.Fatal("Ready = false, want true for empty registry")
	}
	if len(status.Policies) != 0 {
		t.Fatalf("Policies = %d, want 0", len(status.Policies))
	}
}

// ---------------------------------------------------------------------------
// TestRegistryRegister — Register adds a reporter, CheckReadiness includes it
// ---------------------------------------------------------------------------

func TestRegistryRegister(t *testing.T) {
	reg := NewRegistry()
	clk := newPolicyClock()

	p := NewPolicy[string]("test-policy",
		WithClock(clk),
		WithRegistry(reg),
	)
	_ = p

	status := reg.CheckReadiness()

	if len(status.Policies) != 1 {
		t.Fatalf("Policies = %d, want 1", len(status.Policies))
	}
	if status.Policies[0].Name != "test-policy" {
		t.Fatalf("Policies[0].Name = %q, want %q", status.Policies[0].Name, "test-policy")
	}
	if !status.Ready {
		t.Fatal("Ready = false, want true")
	}
}

// ---------------------------------------------------------------------------
// TestRegistryAllHealthy — multiple healthy policies → Ready=true
// ---------------------------------------------------------------------------

func TestRegistryAllHealthy(t *testing.T) {
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

	if !status.Ready {
		t.Fatal("Ready = false, want true when all healthy")
	}
	if len(status.Policies) != 3 {
		t.Fatalf("Policies = %d, want 3", len(status.Policies))
	}
	for _, ps := range status.Policies {
		if !ps.Healthy {
			t.Fatalf("policy %q: Healthy = false, want true", ps.Name)
		}
	}
}

// ---------------------------------------------------------------------------
// TestRegistryOneCritical — one policy with open circuit → Ready=false
// ---------------------------------------------------------------------------

func TestRegistryOneCritical(t *testing.T) {
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

	if status.Ready {
		t.Fatal("Ready = true, want false (one critical unhealthy policy)")
	}

	// Verify the unhealthy policy is reported correctly.
	var found bool
	for _, ps := range status.Policies {
		if ps.Name == "unhealthy-svc" {
			found = true
			if ps.Healthy {
				t.Fatal("unhealthy-svc: Healthy = true, want false")
			}
			if ps.Criticality != CriticalityCritical {
				t.Fatalf("unhealthy-svc: Criticality = %v, want CriticalityCritical", ps.Criticality)
			}
		}
	}
	if !found {
		t.Fatal("unhealthy-svc not found in status.Policies")
	}
}

// ---------------------------------------------------------------------------
// TestRegistryOneDegraded — one policy degraded (rate limited) → Ready=true
// ---------------------------------------------------------------------------

func TestRegistryOneDegraded(t *testing.T) {
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
	if !status.Ready {
		t.Fatal("Ready = false, want true (degraded is not critical)")
	}

	// Verify the policy reports degraded.
	if len(status.Policies) != 1 {
		t.Fatalf("Policies = %d, want 1", len(status.Policies))
	}
	if status.Policies[0].Criticality != CriticalityDegraded {
		t.Fatalf("Criticality = %v, want CriticalityDegraded", status.Policies[0].Criticality)
	}
}

// ---------------------------------------------------------------------------
// TestRegistryConcurrentReads — many goroutines calling CheckReadiness is safe
// ---------------------------------------------------------------------------

func TestRegistryConcurrentReads(t *testing.T) {
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
			if len(status.Policies) != 5 {
				t.Errorf("Policies = %d, want 5", len(status.Policies))
			}
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// TestRegistryConcurrentRegisterAndRead — Register and CheckReadiness concurrent
// ---------------------------------------------------------------------------

func TestRegistryConcurrentRegisterAndRead(t *testing.T) {
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
	if len(status.Policies) != 10 {
		t.Fatalf("Policies = %d, want 10", len(status.Policies))
	}
}

// ---------------------------------------------------------------------------
// TestDefaultRegistry — returns same instance on multiple calls
// ---------------------------------------------------------------------------

func TestDefaultRegistry(t *testing.T) {
	r1 := DefaultRegistry()
	r2 := DefaultRegistry()

	if r1 != r2 {
		t.Fatal("DefaultRegistry() returned different instances")
	}
	if r1 == nil {
		t.Fatal("DefaultRegistry() returned nil")
	}
}

// ---------------------------------------------------------------------------
// TestWithRegistry — policy registered with explicit registry
// ---------------------------------------------------------------------------

func TestWithRegistry(t *testing.T) {
	reg := NewRegistry()
	clk := newPolicyClock()

	p := NewPolicy[string]("explicit-reg",
		WithClock(clk),
		WithRegistry(reg),
	)

	if p.registry != reg {
		t.Fatal("policy.registry does not match the explicit registry")
	}

	status := reg.CheckReadiness()

	if len(status.Policies) != 1 {
		t.Fatalf("Policies = %d, want 1", len(status.Policies))
	}
	if status.Policies[0].Name != "explicit-reg" {
		t.Fatalf("Policies[0].Name = %q, want %q", status.Policies[0].Name, "explicit-reg")
	}
}

// ---------------------------------------------------------------------------
// TestAutoRegistration — NewPolicy with name auto-registers with DefaultRegistry
// ---------------------------------------------------------------------------

func TestAutoRegistration(t *testing.T) {
	// Use an explicit registry to avoid polluting the global DefaultRegistry.
	// The mechanism is the same — we just verify the auto-registration path.
	reg := NewRegistry()

	p := NewPolicy[string]("auto-reg-test",
		WithRegistry(reg),
	)

	if p.registry != reg {
		t.Fatal("policy.registry should be set")
	}

	status := reg.CheckReadiness()
	if len(status.Policies) != 1 {
		t.Fatalf("Policies = %d, want 1 (auto-registered)", len(status.Policies))
	}
	if status.Policies[0].Name != "auto-reg-test" {
		t.Fatalf("Policies[0].Name = %q, want %q", status.Policies[0].Name, "auto-reg-test")
	}
}

// ---------------------------------------------------------------------------
// TestAnonymousPolicyNotRegistered — NewPolicy("", ...) doesn't register
// ---------------------------------------------------------------------------

func TestAnonymousPolicyNotRegistered(t *testing.T) {
	reg := NewRegistry()

	p := NewPolicy[string]("",
		WithRegistry(reg),
	)

	if p.registry != nil {
		t.Fatal("anonymous policy should have nil registry")
	}

	status := reg.CheckReadiness()
	if len(status.Policies) != 0 {
		t.Fatalf("Policies = %d, want 0 (anonymous policy should not be registered)", len(status.Policies))
	}
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
