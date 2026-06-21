package r8e

import "slices"

// ---------------------------------------------------------------------------
// HealthReporter interface
// ---------------------------------------------------------------------------.

type (
	// HealthReporter is implemented by all Policy[T] instances.
	// The interface is non-generic, allowing policies with different type
	// parameters to be used as dependencies of one another.
	HealthReporter interface {
		// Name returns the policy's name.
		Name() string
		// HealthStatus returns the current health state of the policy.
		HealthStatus() PolicyStatus
	}

	// Criticality represents how a pattern's unhealthy state affects readiness.
	Criticality int

	// Condition is a single active degradation of a policy. The set of valid
	// values is the Condition* constants; State and Conditions use this type so
	// the producing code and the severity table share one source of truth.
	Condition string

	// PolicyStatus represents the current health state of a policy.
	PolicyStatus struct {
		// Name is the policy name.
		Name string `json:"name"`
		// State is a deterministic summary derived from Conditions (most-severe
		// wins); ConditionHealthy when there are none.
		State Condition `json:"state"`
		// Conditions lists every active degradation, order-independent and
		// complete (e.g. ["rate_limited","bulkhead_full"]); empty when healthy.
		Conditions []Condition `json:"conditions,omitempty"`
		// Dependencies holds the health of declared sub-dependencies.
		Dependencies []PolicyStatus `json:"dependencies,omitempty"`
		// Criticality is the worst severity currently observed.
		Criticality Criticality `json:"criticality"`
		// Healthy is false when the policy cannot reliably serve (e.g. its
		// circuit breaker is open).
		Healthy bool `json:"healthy"`
		// AffectsReadiness reports whether this policy gates Kubernetes
		// readiness (see WithReadinessImpact). False by default.
		AffectsReadiness bool `json:"affects_readiness"`
	}
)

const (
	// CriticalityNone means the pattern has no persistent health state.
	CriticalityNone Criticality = iota
	// CriticalityDegraded means the service can still serve but is impaired.
	CriticalityDegraded
	// CriticalityCritical means the service cannot reliably serve requests.
	CriticalityCritical

	// ConditionHealthy is the State when no degradation is active.
	ConditionHealthy Condition = "healthy"
	// ConditionCircuitOpen means the circuit breaker is open (critical).
	ConditionCircuitOpen Condition = "circuit_open"
	// ConditionRateLimited means the rate limiter is saturated (degraded).
	ConditionRateLimited Condition = "rate_limited"
	// ConditionBulkheadFull means the bulkhead is at capacity (degraded).
	ConditionBulkheadFull Condition = "bulkhead_full"
	// ConditionDependencyDegraded means a critical dependency is unhealthy.
	ConditionDependencyDegraded Condition = "dependency_degraded"
	// ConditionCircuitHalfOpen means the breaker is probing recovery.
	ConditionCircuitHalfOpen Condition = "circuit_half_open"
	// ConditionRetryBudgetExhausted means the retry budget is throttling
	// retries (degraded); first attempts still flow.
	ConditionRetryBudgetExhausted Condition = "retry_budget_exhausted"
	// ConditionConcurrencyLimited means the adaptive concurrency limiter is at
	// its current limit (degraded); it is shedding excess concurrency.
	ConditionConcurrencyLimited Condition = "concurrency_limited"
)

// conditionSeverity is the single source of truth for the degradation model: it
// pairs each Condition with its Criticality, ordered from most to least severe.
// Both the most-severe State summary (summarizeState) and the per-condition
// criticality (criticalityOf) derive from it, so the producing code, the state
// ordering, and the severity cannot drift apart. ConditionHealthy is the
// absence of degradation and is intentionally not listed.
//
//nolint:gochecknoglobals // read-only lookup table, never mutated after init
var conditionSeverity = []struct {
	Condition   Condition
	Criticality Criticality
}{
	{ConditionCircuitOpen, CriticalityCritical},
	{ConditionRateLimited, CriticalityDegraded},
	{ConditionBulkheadFull, CriticalityDegraded},
	{ConditionConcurrencyLimited, CriticalityDegraded},
	{ConditionRetryBudgetExhausted, CriticalityDegraded},
	{ConditionDependencyDegraded, CriticalityDegraded},
	{ConditionCircuitHalfOpen, CriticalityNone},
}

// String returns the criticality level as a human-readable string.
func (c Criticality) String() string {
	switch c {
	case CriticalityDegraded:
		return "degraded"
	case CriticalityCritical:
		return "critical"
	default:
		return "none"
	}
}

// ---------------------------------------------------------------------------
// HealthStatus on Policy[T]
// ---------------------------------------------------------------------------.

// HealthStatus derives the policy's current health by inspecting stateful
// patterns. Every active degradation is recorded in Conditions (complete and
// order-independent); State, Criticality, and Healthy are all derived from that
// one set here, so they are mutually consistent for any status this method
// produces. (The fields are exported for JSON; a hand-built PolicyStatus is not
// bound by that invariant.)
func (p *Policy[T]) HealthStatus() PolicyStatus {
	conditions, deps := p.collectConditions()

	worst := CriticalityNone
	for _, c := range conditions {
		if cc := criticalityOf(c); cc > worst {
			worst = cc
		}
	}

	return PolicyStatus{
		Name:             p.name,
		State:            summarizeState(conditions),
		Conditions:       conditions,
		Dependencies:     deps,
		Criticality:      worst,
		Healthy:          worst < CriticalityCritical,
		AffectsReadiness: p.affectsReadiness,
	}
}

// collectConditions inspects every stateful pattern and returns the active
// degradations together with the resolved health of each declared dependency.
func (p *Policy[T]) collectConditions() ([]Condition, []PolicyStatus) {
	var conditions []Condition

	// Circuit breaker — open is critical, half-open is recovering.
	if p.circuitBreaker != nil {
		if cond, active := circuitCondition(p.circuitBreaker.State()); active {
			conditions = append(conditions, cond)
		}
	}

	// Rate limiter — degraded (not unhealthy on its own).
	if p.rateLimiter != nil && p.rateLimiter.Saturated() {
		conditions = append(conditions, ConditionRateLimited)
	}

	// Bulkhead — degraded (not unhealthy on its own).
	if p.bulkhead != nil && p.bulkhead.Full() {
		conditions = append(conditions, ConditionBulkheadFull)
	}

	// Adaptive concurrency limiter — degraded when saturated (shedding load).
	if p.adaptive != nil && p.adaptive.Saturated() {
		conditions = append(conditions, ConditionConcurrencyLimited)
	}

	// Retry budget — degraded; retries are throttled but first attempts flow.
	if p.retryBudget != nil && p.retryBudget.Exhausted() {
		conditions = append(conditions, ConditionRetryBudgetExhausted)
	}

	// Dependencies — a critically-down dependency degrades this policy.
	deps := make([]PolicyStatus, 0, len(p.deps))

	for _, dep := range p.deps {
		depStatus := dep.HealthStatus()
		deps = append(deps, depStatus)

		if depStatus.criticallyDown() {
			conditions = append(conditions, ConditionDependencyDegraded)
		}
	}

	return conditions, deps
}

// circuitCondition maps a circuit-breaker state to its health Condition. The
// second result is false when the breaker contributes no condition (closed).
// An unrecognised state fails safe — it is reported as open rather than
// silently treated as healthy — so a new breaker state can never quietly make a
// down policy look ready.
func circuitCondition(state CircuitState) (Condition, bool) {
	switch state {
	case CircuitOpen:
		return ConditionCircuitOpen, true
	case CircuitHalfOpen:
		return ConditionCircuitHalfOpen, true
	case CircuitClosed:
		return ConditionHealthy, false
	default:
		return ConditionCircuitOpen, true
	}
}

// criticallyDown reports whether the policy is critically unhealthy — the
// single predicate that gates readiness and marks a dependency as degrading.
func (s *PolicyStatus) criticallyDown() bool {
	return !s.Healthy && s.Criticality == CriticalityCritical
}

// criticalityOf returns the severity of a single active condition from the
// shared conditionSeverity table. A condition that is reported but absent from
// the table is treated as a degradation (never CriticalityNone) — the same
// fail-safe direction summarizeState takes, so an untabled-but-active condition
// can never be summarised as healthy. It is only ever called on members of a
// Conditions slice (ConditionHealthy never appears there).
func criticalityOf(c Condition) Criticality {
	for _, spec := range conditionSeverity {
		if spec.Condition == c {
			return spec.Criticality
		}
	}

	return CriticalityDegraded
}

// summarizeState returns the most severe active condition. It returns
// ConditionHealthy only when there are no conditions; a non-empty set with no
// table entry falls back to the first reported condition rather than
// masquerading as healthy.
func summarizeState(conditions []Condition) Condition {
	for _, spec := range conditionSeverity {
		if slices.Contains(conditions, spec.Condition) {
			return spec.Condition
		}
	}

	if len(conditions) > 0 {
		return conditions[0]
	}

	return ConditionHealthy
}
