package r8e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openCircuit drives p's breaker open with a single failure (FailureThreshold 1).
func openCircuit(t *testing.T, p *Policy[string]) {
	t.Helper()

	_, _ = p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("boom")
	})
}

// TestReadinessDefaultDoesNotGate is the key v0.3.0 behaviour: a critically
// unhealthy policy does NOT remove the pod from rotation unless it opted in.
func TestReadinessDefaultDoesNotGate(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	p := NewPolicy[string]("crit-no-gate",
		WithClock(&stubClock{now: time.Now()}),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(1), RecoveryTimeout(time.Hour)),
	)

	openCircuit(t, p)

	status := reg.CheckReadiness()
	require.True(t, status.Ready, "an un-gated critical policy must not flip readiness")

	var found bool
	for _, ps := range status.Policies {
		if ps.Name == "crit-no-gate" {
			found = true

			assert.False(t, ps.Healthy)
			assert.False(t, ps.AffectsReadiness)
			assert.Equal(t, CriticalityCritical, ps.Criticality)
			assert.Equal(t, ConditionCircuitOpen, ps.State)
			assert.Contains(t, ps.Conditions, ConditionCircuitOpen)
		}
	}
	require.True(t, found)
}

func TestReadinessImpactGates(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	p := NewPolicy[string]("crit-gate",
		WithClock(&stubClock{now: time.Now()}),
		WithRegistry(reg),
		WithReadinessImpact(),
		WithCircuitBreaker(FailureThreshold(1), RecoveryTimeout(time.Hour)),
	)

	openCircuit(t, p)

	require.False(t, reg.CheckReadiness().Ready)
}

func TestRegistryHealthAggregation(t *testing.T) {
	t.Parallel()

	// Empty registry is healthy.
	assert.Equal(t, HealthHealthy, NewRegistry().Health().Status)

	// All-healthy policy → "healthy".
	reg := NewRegistry()
	_ = NewPolicy[string]("ok", WithRegistry(reg), WithCircuitBreaker())
	assert.Equal(t, HealthHealthy, reg.Health().Status)

	// A saturated rate limiter → "degraded" (not unhealthy).
	degraded := NewPolicy[string]("degraded",
		WithClock(&stubClock{now: time.Now()}),
		WithRegistry(reg),
		WithRateLimit(1),
	)
	_, _ = degraded.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	}) // consumes the only token → saturated

	report := reg.Health()
	assert.Equal(t, HealthDegraded, report.Status)
	assert.Len(t, report.Policies, 2)

	// An open breaker → "unhealthy".
	crit := NewPolicy[string]("crit",
		WithClock(&stubClock{now: time.Now()}),
		WithRegistry(reg),
		WithCircuitBreaker(FailureThreshold(1), RecoveryTimeout(time.Hour)),
	)
	openCircuit(t, crit)

	assert.Equal(t, HealthUnhealthy, reg.Health().Status)
}

func TestSummarizeStateDeterministic(t *testing.T) {
	t.Parallel()

	// Cases cover every adjacent pair in conditionSeverity (so a reordering is
	// caught) plus order-independence and the empty case.
	tests := map[string]struct {
		conditions []Condition
		want       Condition
	}{
		"none":                     {nil, ConditionHealthy},
		"single":                   {[]Condition{ConditionRateLimited}, ConditionRateLimited},
		"open over rate":           {[]Condition{ConditionRateLimited, ConditionCircuitOpen}, ConditionCircuitOpen},
		"rate over bulkhead":       {[]Condition{ConditionBulkheadFull, ConditionRateLimited}, ConditionRateLimited},
		"bulkhead over dependency": {[]Condition{ConditionDependencyDegraded, ConditionBulkheadFull}, ConditionBulkheadFull},
		"dependency over halfopen": {[]Condition{ConditionCircuitHalfOpen, ConditionDependencyDegraded}, ConditionDependencyDegraded},
		"order independent":        {[]Condition{ConditionRateLimited, ConditionBulkheadFull}, ConditionRateLimited},
		"unknown not healthy":      {[]Condition{Condition("surprise")}, Condition("surprise")},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, summarizeState(tt.conditions))
		})
	}
}

// TestHealthStatusConditionsComplete drives TWO simultaneous degradations
// (rate-limited + bulkhead-full) and verifies Conditions reports both, while
// State summarises to the most severe.
func TestHealthStatusConditionsComplete(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("multi",
		WithClock(&stubClock{now: time.Now()}),
		WithRegistry(NewRegistry()),
		WithReadinessImpact(),
		WithRateLimit(1),
		WithBulkhead(1),
	)

	// Saturate the rate limiter and fill the bulkhead directly so both
	// conditions are simultaneously active (the chain would reject at the rate
	// limiter before reaching the bulkhead, so we exercise the components).
	require.NoError(t, p.rateLimiter.Allow(context.Background()))
	require.NoError(t, p.bulkhead.Acquire(context.Background()))

	status := p.HealthStatus()
	assert.True(t, status.AffectsReadiness)
	assert.Equal(t, CriticalityDegraded, status.Criticality)
	assert.Contains(t, status.Conditions, ConditionRateLimited)
	assert.Contains(t, status.Conditions, ConditionBulkheadFull)
	assert.Len(t, status.Conditions, 2)
	assert.Equal(t, ConditionRateLimited, status.State, "rate_limited outranks bulkhead_full")
}

// TestReadinessImpactDegradedDoesNotGate verifies that a readiness-impacting
// policy which is only DEGRADED (not critically down) keeps the pod in
// rotation. The criticality operand of the gate predicate itself is pinned
// directly by TestCriticallyDown.
func TestReadinessImpactDegradedDoesNotGate(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	p := NewPolicy[string]("gated-degraded",
		WithClock(&stubClock{now: time.Now()}),
		WithRegistry(reg),
		WithReadinessImpact(),
		WithRateLimit(1),
	)

	require.NoError(t, p.rateLimiter.Allow(context.Background())) // saturate → degraded

	status := reg.CheckReadiness()
	require.True(t, status.Ready,
		"a readiness-impacting policy that is only degraded must not gate")
}

// TestCriticallyDown pins BOTH operands of the readiness gate predicate. A
// mutation dropping either operand (e.g. `return !s.Healthy`) flips one of
// these rows, so the suite no longer passes green with a broadened gate.
func TestCriticallyDown(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		healthy     bool
		criticality Criticality
		want        bool
	}{
		"unhealthy and critical is down":   {false, CriticalityCritical, true},
		"unhealthy but only degraded":      {false, CriticalityDegraded, false},
		"critical but reported healthy":    {true, CriticalityCritical, false},
		"healthy with no criticality":      {true, CriticalityNone, false},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			ps := PolicyStatus{Healthy: tt.healthy, Criticality: tt.criticality}
			assert.Equal(t, tt.want, ps.criticallyDown())
		})
	}
}

// TestCircuitCondition pins the breaker-state → Condition mapping, including the
// fail-safe default: an unrecognised state must report open, never healthy.
func TestCircuitCondition(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		state      CircuitState
		wantCond   Condition
		wantActive bool
	}{
		"open is a critical condition":   {CircuitOpen, ConditionCircuitOpen, true},
		"half-open is recovering":        {CircuitHalfOpen, ConditionCircuitHalfOpen, true},
		"closed contributes nothing":     {CircuitClosed, ConditionHealthy, false},
		"unknown state fails safe →open": {CircuitState("bogus"), ConditionCircuitOpen, true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cond, active := circuitCondition(tt.state)
			assert.Equal(t, tt.wantActive, active)

			if active {
				assert.Equal(t, tt.wantCond, cond)
			}
		})
	}
}

// TestCriticalityOf pins the Condition→Criticality lookup, including the
// fail-safe fallback: a condition absent from the table is treated as a
// degradation (never CriticalityNone), so it cannot be summarised as healthy.
func TestCriticalityOf(t *testing.T) {
	t.Parallel()

	assert.Equal(t, CriticalityCritical, criticalityOf(ConditionCircuitOpen))
	assert.Equal(t, CriticalityDegraded, criticalityOf(ConditionRateLimited))
	assert.Equal(t, CriticalityNone, criticalityOf(ConditionCircuitHalfOpen))
	assert.Equal(t, CriticalityDegraded, criticalityOf(Condition("unknown")))
}

// TestConditionSeverityComplete checks the invariants this test CAN enforce on
// conditionSeverity: every degradation Condition this test knows about is
// present, no extra entries exist, ConditionHealthy is absent, and the table is
// ordered most-severe-first so summarizeState's precedence (by position) agrees
// with criticalityOf's severity (by value). It does NOT, by itself, prove a
// future Condition constant was given a severity — that forward case is handled
// defensively by criticalityOf's degraded fallback instead.
func TestConditionSeverityComplete(t *testing.T) {
	t.Parallel()

	degradations := []Condition{
		ConditionCircuitOpen,
		ConditionRateLimited,
		ConditionBulkheadFull,
		ConditionConcurrencyLimited,
		ConditionThrottling,
		ConditionSLOBurning,
		ConditionRetryBudgetExhausted,
		ConditionConcurrencyBudgetExhausted,
		ConditionDependencyDegraded,
		ConditionCircuitHalfOpen,
	}

	for _, c := range degradations {
		var found bool

		for _, spec := range conditionSeverity {
			if spec.Condition == c {
				found = true
			}
		}

		assert.True(t, found, "condition %q missing from conditionSeverity", c)
	}

	assert.Len(t, conditionSeverity, len(degradations),
		"conditionSeverity has entries this test does not cover")

	for _, spec := range conditionSeverity {
		assert.NotEqual(t, ConditionHealthy, spec.Condition,
			"ConditionHealthy must not appear in the severity table")
	}

	// Precedence order (position) must track severity (value), so the two
	// derivations from this one table cannot disagree about which outranks which.
	for i := 1; i < len(conditionSeverity); i++ {
		assert.GreaterOrEqual(t,
			conditionSeverity[i-1].Criticality, conditionSeverity[i].Criticality,
			"conditionSeverity must be ordered by non-increasing criticality")
	}
}

// TestRegistryHealthHalfOpenIsHealthy pins the aggregation of a half-open-only
// policy: a recovering breaker contributes ConditionCircuitHalfOpen at
// CriticalityNone, so the aggregate health stays healthy.
func TestRegistryHealthHalfOpenIsHealthy(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	clk := &stubClock{now: time.Now()}
	p := NewPolicy[string]("recovering",
		WithClock(clk),
		WithRegistry(reg),
		WithCircuitBreaker(
			FailureThreshold(1),
			RecoveryTimeout(time.Second),
			HalfOpenMaxAttempts(2),
		),
	)

	openCircuit(t, p)               // one failure → open
	clk.setElapsed(2 * time.Second) // recovery window elapsed

	// A successful probe transitions open→half-open; with HalfOpenMaxAttempts(2)
	// a single success leaves the breaker half-open rather than closing it.
	_, _ = p.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})

	require.Equal(t, ConditionCircuitHalfOpen, p.HealthStatus().State)
	assert.Equal(t, HealthHealthy, reg.Health().Status)
}
