package r8e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TestCriticalityString — all Criticality.String() values
// ---------------------------------------------------------------------------

func TestCriticalityString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		c    Criticality
		want string
	}{
		{"none", CriticalityNone, "none"},
		{"degraded", CriticalityDegraded, "degraded"},
		{"critical", CriticalityCritical, "critical"},
		{"unknown", Criticality(99), "none"}, // unknown falls through to default
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.c.String())
		})
	}
}

// ---------------------------------------------------------------------------
// TestHealthyPolicyNoPatterns — Policy with no patterns reports healthy
// ---------------------------------------------------------------------------

func TestHealthyPolicyNoPatterns(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("empty")

	status := p.HealthStatus()

	require.Equal(t, "empty", status.Name)
	require.True(t, status.Healthy)
	require.Equal(t, CriticalityNone, status.Criticality)
	require.Equal(t, ConditionHealthy, status.State)
	require.Empty(t, status.Dependencies)
}

// ---------------------------------------------------------------------------
// TestHealthyPolicyCircuitBreakerClosed — CB closed → healthy, CriticalityNone
// ---------------------------------------------------------------------------

func TestHealthyPolicyCircuitBreakerClosed(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()

	p := NewPolicy[string]("cb-closed",
		WithClock(clk),
		WithCircuitBreaker(FailureThreshold(5)),
	)

	status := p.HealthStatus()

	require.True(t, status.Healthy)
	require.Equal(t, CriticalityNone, status.Criticality)
	require.Equal(t, ConditionHealthy, status.State)
}

// ---------------------------------------------------------------------------
// TestUnhealthyCircuitBreakerOpen — CB open → unhealthy, CriticalityCritical
// ---------------------------------------------------------------------------

func TestUnhealthyCircuitBreakerOpen(t *testing.T) {
	t.Parallel()

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

	require.False(t, status.Healthy)
	require.Equal(t, CriticalityCritical, status.Criticality)
	require.Equal(t, ConditionCircuitOpen, status.State)
}

// ---------------------------------------------------------------------------
// TestCircuitBreakerHalfOpen — CB half-open → healthy, state
// "circuit_half_open"
// ---------------------------------------------------------------------------

func TestCircuitBreakerHalfOpen(t *testing.T) {
	t.Parallel()

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

	require.True(t, status.Healthy)
	require.Equal(t, ConditionCircuitHalfOpen, status.State)
}

// ---------------------------------------------------------------------------
// TestRateLimiterSaturated — RL saturated → CriticalityDegraded, "rate_limited"
// ---------------------------------------------------------------------------

func TestRateLimiterSaturated(t *testing.T) {
	t.Parallel()

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

	require.Equal(t, CriticalityDegraded, status.Criticality)
	require.Equal(t, ConditionRateLimited, status.State)
	// Rate limiter saturation alone doesn't make policy unhealthy.
	require.True(t, status.Healthy)
}

// ---------------------------------------------------------------------------
// TestBulkheadFull — BH full → CriticalityDegraded, "bulkhead_full"
// ---------------------------------------------------------------------------

func TestBulkheadFull(t *testing.T) {
	t.Parallel()

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

	require.Equal(t, CriticalityDegraded, status.Criticality)
	require.Equal(t, ConditionBulkheadFull, status.State)
	require.True(t, status.Healthy)
}

// ---------------------------------------------------------------------------
// TestCircuitOpenOverridesRateLimited — CB open + RL saturated →
// CriticalityCritical
// ---------------------------------------------------------------------------

func TestCircuitOpenOverridesRateLimited(t *testing.T) {
	t.Parallel()

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

	require.False(t, status.Healthy)
	require.Equal(t, CriticalityCritical, status.Criticality)
	require.Equal(t, ConditionCircuitOpen, status.State)
}

// ---------------------------------------------------------------------------
// TestDependencyPropagation — parent depends on child; child CB open →
// parent degraded
// ---------------------------------------------------------------------------

func TestDependencyPropagation(t *testing.T) {
	t.Parallel()

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

	require.Len(t, status.Dependencies, 1)

	depStatus := status.Dependencies[0]
	require.Equal(t, "child", depStatus.Name)
	require.False(t, depStatus.Healthy)
	require.Equal(t, CriticalityCritical, depStatus.Criticality)

	// Parent should be degraded (not critical) due to child's critical status.
	require.GreaterOrEqual(t, status.Criticality, CriticalityDegraded)
}

// ---------------------------------------------------------------------------
// TestDependsOnOption — DependsOn option wires dependencies
// ---------------------------------------------------------------------------

func TestDependsOnOption(t *testing.T) {
	t.Parallel()

	dep1 := NewPolicy[string]("dep1")
	dep2 := NewPolicy[int]("dep2") // Different type parameter!

	p := NewPolicy[string]("main",
		DependsOn(dep1, dep2),
	)

	status := p.HealthStatus()

	require.Len(t, status.Dependencies, 2)
	require.Equal(t, "dep1", status.Dependencies[0].Name)
	require.Equal(t, "dep2", status.Dependencies[1].Name)

	// Both dependencies are healthy, so they must contribute NO condition and
	// leave the parent healthy. This pins the criticallyDown() guard in
	// collectConditions: appending ConditionDependencyDegraded unconditionally
	// would make Conditions non-empty and Criticality degraded here.
	assert.Empty(t, status.Conditions)
	assert.Equal(t, CriticalityNone, status.Criticality)
	assert.True(t, status.Healthy)
}

// ---------------------------------------------------------------------------
// TestHealthReporterInterface — compile-time check that Policy implements
// HealthReporter
// ---------------------------------------------------------------------------

func TestHealthReporterInterface(t *testing.T) {
	t.Parallel()

	var _ HealthReporter = NewPolicy[string]("interface-check")
	var _ HealthReporter = NewPolicy[int]("interface-check-int")

	// If this compiles, the interface is satisfied.
	p := NewPolicy[string]("cast")
	var hr HealthReporter = p
	require.Equal(t, "cast", hr.Name())
	status := hr.HealthStatus()
	require.True(t, status.Healthy)
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
