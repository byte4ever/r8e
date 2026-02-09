package r8e

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// TestCriticalityString — all Criticality.String() values
// ---------------------------------------------------------------------------

func TestCriticalityString(t *testing.T) {
	tests := []struct {
		c    Criticality
		want string
	}{
		{CriticalityNone, "none"},
		{CriticalityDegraded, "degraded"},
		{CriticalityCritical, "critical"},
		{Criticality(99), "none"}, // unknown falls through to default
	}

	for _, tt := range tests {
		if got := tt.c.String(); got != tt.want {
			t.Errorf(
				"Criticality(%d).String() = %q, want %q",
				tt.c,
				got,
				tt.want,
			)
		}
	}
}

// ---------------------------------------------------------------------------
// TestHealthyPolicyNoPatterns — Policy with no patterns reports healthy
// ---------------------------------------------------------------------------

func TestHealthyPolicyNoPatterns(t *testing.T) {
	p := NewPolicy[string]("empty")

	status := p.HealthStatus()

	if status.Name != "empty" {
		t.Fatalf("Name = %q, want %q", status.Name, "empty")
	}
	if !status.Healthy {
		t.Fatal("Healthy = false, want true")
	}
	if status.Criticality != CriticalityNone {
		t.Fatalf("Criticality = %v, want CriticalityNone", status.Criticality)
	}
	if status.State != "healthy" {
		t.Fatalf("State = %q, want %q", status.State, "healthy")
	}
	if len(status.Dependencies) != 0 {
		t.Fatalf("Dependencies = %v, want empty", status.Dependencies)
	}
}

// ---------------------------------------------------------------------------
// TestHealthyPolicyCircuitBreakerClosed — CB closed → healthy, CriticalityNone
// ---------------------------------------------------------------------------

func TestHealthyPolicyCircuitBreakerClosed(t *testing.T) {
	clk := newPolicyClock()

	p := NewPolicy[string]("cb-closed",
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(5)),
	)

	status := p.HealthStatus()

	if !status.Healthy {
		t.Fatal("Healthy = false, want true")
	}
	if status.Criticality != CriticalityNone {
		t.Fatalf("Criticality = %v, want CriticalityNone", status.Criticality)
	}
	if status.State != "healthy" {
		t.Fatalf("State = %q, want %q", status.State, "healthy")
	}
}

// ---------------------------------------------------------------------------
// TestUnhealthyCircuitBreakerOpen — CB open → unhealthy, CriticalityCritical
// ---------------------------------------------------------------------------

func TestUnhealthyCircuitBreakerOpen(t *testing.T) {
	clk := newPolicyClock()

	p := NewPolicy[string]("cb-open",
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
	)

	ctx := context.Background()

	// Drive circuit breaker to open with 2 failures.
	for range 2 {
		_, _ = p.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("fail")
		})
	}

	status := p.HealthStatus()

	if status.Healthy {
		t.Fatal("Healthy = true, want false")
	}
	if status.Criticality != CriticalityCritical {
		t.Fatalf(
			"Criticality = %v, want CriticalityCritical",
			status.Criticality,
		)
	}
	if status.State != "circuit_open" {
		t.Fatalf("State = %q, want %q", status.State, "circuit_open")
	}
}

// ---------------------------------------------------------------------------
// TestCircuitBreakerHalfOpen — CB half-open → healthy, state
// "circuit_half_open"
// ---------------------------------------------------------------------------

func TestCircuitBreakerHalfOpen(t *testing.T) {
	clk := newPolicyClock()

	// Use HalfOpenMaxAttempts(2) so that one success keeps it in half_open.
	p := NewPolicy[string](
		"cb-half",
		WithClock(clk),
		WithCircuitBreaker(
			FailureThreshold(2),
			RecoveryTimeout(time.Second),
			HalfOpenMaxAttempts(2),
		),
	)

	ctx := context.Background()

	// Drive circuit breaker to open.
	for range 2 {
		_, _ = p.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("fail")
		})
	}

	// Advance time past recovery timeout.
	clk.advance(2 * time.Second)

	// The CB transitions from open → half_open on the next Allow() call.
	// One successful probe keeps it in half_open (needs 2 to close).
	_, _ = p.Do(ctx, func(_ context.Context) (string, error) {
		return "probe", nil
	})

	status := p.HealthStatus()

	if !status.Healthy {
		t.Fatal("Healthy = false, want true (half_open is recovering)")
	}
	if status.State != "circuit_half_open" {
		t.Fatalf("State = %q, want %q", status.State, "circuit_half_open")
	}
}

// ---------------------------------------------------------------------------
// TestRateLimiterSaturated — RL saturated → CriticalityDegraded, "rate_limited"
// ---------------------------------------------------------------------------

func TestRateLimiterSaturated(t *testing.T) {
	clk := newPolicyClock()

	p := NewPolicy[string]("rl-sat",
		WithClock(clk),
		WithRateLimit(1), // 1 token/sec
	)

	ctx := context.Background()

	// Consume the single token.
	_, _ = p.Do(ctx, func(_ context.Context) (string, error) {
		return "ok", nil
	})

	status := p.HealthStatus()

	if status.Criticality != CriticalityDegraded {
		t.Fatalf(
			"Criticality = %v, want CriticalityDegraded",
			status.Criticality,
		)
	}
	if status.State != "rate_limited" {
		t.Fatalf("State = %q, want %q", status.State, "rate_limited")
	}
	// Rate limiter saturation alone doesn't make policy unhealthy.
	if !status.Healthy {
		t.Fatal("Healthy = false, want true (degraded, not down)")
	}
}

// ---------------------------------------------------------------------------
// TestBulkheadFull — BH full → CriticalityDegraded, "bulkhead_full"
// ---------------------------------------------------------------------------

func TestBulkheadFull(t *testing.T) {
	p := NewPolicy[string]("bh-full",
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
				<-done // Block until released.
				return "first", nil
			},
		)
	}()

	<-started // Wait for goroutine to acquire the slot.

	status := p.HealthStatus()

	close(done) // Release the slot.

	if status.Criticality != CriticalityDegraded {
		t.Fatalf(
			"Criticality = %v, want CriticalityDegraded",
			status.Criticality,
		)
	}
	if status.State != "bulkhead_full" {
		t.Fatalf("State = %q, want %q", status.State, "bulkhead_full")
	}
	if !status.Healthy {
		t.Fatal("Healthy = false, want true (degraded, not down)")
	}
}

// ---------------------------------------------------------------------------
// TestCircuitOpenOverridesRateLimited — CB open + RL saturated →
// CriticalityCritical
// ---------------------------------------------------------------------------

func TestCircuitOpenOverridesRateLimited(t *testing.T) {
	clk := newPolicyClock()

	p := NewPolicy[string]("cb-rl",
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
		WithRateLimit(1),
	)

	ctx := context.Background()

	// Consume rate limit token AND cause circuit breaker to open.
	// First call consumes token and records failure.
	_, _ = p.Do(ctx, func(_ context.Context) (string, error) {
		return "", errors.New("fail")
	})
	// Replenish a token so second call goes through circuit breaker.
	clk.advance(2 * time.Second)
	// Second failure opens circuit.
	_, _ = p.Do(ctx, func(_ context.Context) (string, error) {
		return "", errors.New("fail")
	})

	status := p.HealthStatus()

	if status.Healthy {
		t.Fatal("Healthy = true, want false")
	}
	if status.Criticality != CriticalityCritical {
		t.Fatalf(
			"Criticality = %v, want CriticalityCritical (circuit open overrides rate limited)",
			status.Criticality,
		)
	}
	if status.State != "circuit_open" {
		t.Fatalf("State = %q, want %q", status.State, "circuit_open")
	}
}

// ---------------------------------------------------------------------------
// TestDependencyPropagation — parent depends on child; child CB open →
// parent degraded
// ---------------------------------------------------------------------------

func TestDependencyPropagation(t *testing.T) {
	clk := newPolicyClock()

	child := NewPolicy[string]("child",
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(2), RecoveryTimeout(time.Hour)),
	)

	ctx := context.Background()

	// Open child's circuit breaker.
	for range 2 {
		_, _ = child.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("fail")
		})
	}

	parent := NewPolicy[string]("parent",
		DependsOn(child),
	)

	status := parent.HealthStatus()

	if len(status.Dependencies) != 1 {
		t.Fatalf("Dependencies = %d, want 1", len(status.Dependencies))
	}

	depStatus := status.Dependencies[0]
	if depStatus.Name != "child" {
		t.Fatalf("dep.Name = %q, want %q", depStatus.Name, "child")
	}
	if depStatus.Healthy {
		t.Fatal("dep.Healthy = true, want false")
	}
	if depStatus.Criticality != CriticalityCritical {
		t.Fatalf(
			"dep.Criticality = %v, want CriticalityCritical",
			depStatus.Criticality,
		)
	}

	// Parent should be degraded (not critical) due to child's critical status.
	if status.Criticality < CriticalityDegraded {
		t.Fatalf(
			"parent Criticality = %v, want >= CriticalityDegraded",
			status.Criticality,
		)
	}
}

// ---------------------------------------------------------------------------
// TestDependsOnOption — DependsOn option wires dependencies
// ---------------------------------------------------------------------------

func TestDependsOnOption(t *testing.T) {
	dep1 := NewPolicy[string]("dep1")
	dep2 := NewPolicy[int]("dep2") // Different type parameter!

	p := NewPolicy[string]("main",
		DependsOn(dep1, dep2),
	)

	status := p.HealthStatus()

	if len(status.Dependencies) != 2 {
		t.Fatalf("Dependencies = %d, want 2", len(status.Dependencies))
	}
	if status.Dependencies[0].Name != "dep1" {
		t.Fatalf(
			"dep[0].Name = %q, want %q",
			status.Dependencies[0].Name,
			"dep1",
		)
	}
	if status.Dependencies[1].Name != "dep2" {
		t.Fatalf(
			"dep[1].Name = %q, want %q",
			status.Dependencies[1].Name,
			"dep2",
		)
	}
}

// ---------------------------------------------------------------------------
// TestHealthReporterInterface — compile-time check that Policy implements
// HealthReporter
// ---------------------------------------------------------------------------

func TestHealthReporterInterface(t *testing.T) {
	var _ HealthReporter = NewPolicy[string]("interface-check")
	var _ HealthReporter = NewPolicy[int]("interface-check-int")

	// If this compiles, the interface is satisfied.
	p := NewPolicy[string]("cast")
	var hr HealthReporter = p
	if hr.Name() != "cast" {
		t.Fatalf("Name() via HealthReporter = %q, want %q", hr.Name(), "cast")
	}
	status := hr.HealthStatus()
	if !status.Healthy {
		t.Fatal("HealthStatus().Healthy = false, want true")
	}
}

// ---------------------------------------------------------------------------
// BenchmarkHealthStatus — benchmark HealthStatus() call
// ---------------------------------------------------------------------------

func BenchmarkHealthStatus(b *testing.B) {
	clk := newPolicyClock()

	child := NewPolicy[string]("child",
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(5)),
		WithRateLimit(100),
	)

	p := NewPolicy[string]("bench-health",
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(5)),
		WithRateLimit(100),
		WithBulkhead(10),
		DependsOn(child),
	)

	for b.Loop() {
		_ = p.HealthStatus()
	}
}
