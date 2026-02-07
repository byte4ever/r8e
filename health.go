package r8e

import "time"

// ---------------------------------------------------------------------------
// HealthReporter interface
// ---------------------------------------------------------------------------

// HealthReporter is implemented by all Policy[T] instances.
// The interface is non-generic, allowing policies with different type
// parameters to be used as dependencies of one another.
type HealthReporter interface {
	Name() string
	HealthStatus() PolicyStatus
}

// ---------------------------------------------------------------------------
// Criticality
// ---------------------------------------------------------------------------

// Criticality represents how a pattern's unhealthy state affects readiness.
type Criticality int

const (
	// CriticalityNone means the pattern has no persistent health state.
	CriticalityNone Criticality = iota
	// CriticalityDegraded means the service can still serve but is impaired.
	CriticalityDegraded
	// CriticalityCritical means the service cannot reliably serve requests.
	CriticalityCritical
)

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
// PolicyStatus
// ---------------------------------------------------------------------------

// PolicyStatus represents the current health state of a policy.
type PolicyStatus struct {
	Name         string         `json:"name"`
	Healthy      bool           `json:"healthy"`
	Criticality  Criticality    `json:"criticality"`
	State        string         `json:"state"`
	ServingStale bool           `json:"serving_stale"`
	StaleAge     time.Duration  `json:"stale_age"`
	Dependencies []PolicyStatus `json:"dependencies,omitempty"`
}

// ---------------------------------------------------------------------------
// HealthStatus on Policy[T]
// ---------------------------------------------------------------------------

// HealthStatus derives the policy's current health by inspecting stateful patterns.
func (p *Policy[T]) HealthStatus() PolicyStatus {
	status := PolicyStatus{
		Name:    p.name,
		Healthy: true,
		State:   "healthy",
	}

	// Circuit breaker — Critical
	if p.cb != nil {
		s := p.cb.State()
		if s == "open" {
			status.Healthy = false
			status.Criticality = CriticalityCritical
			status.State = "circuit_open"
		} else if s == "half_open" {
			status.State = "circuit_half_open"
			// half_open is not unhealthy — it's recovering
		}
	}

	// Rate limiter — Degraded (only if not already Critical)
	if p.rl != nil && p.rl.Saturated() {
		if status.Criticality < CriticalityDegraded {
			status.Criticality = CriticalityDegraded
		}
		if status.Healthy {
			status.State = "rate_limited"
		}
		// Rate limiter saturation alone doesn't make the policy unhealthy
		// (it's degraded, not down). But if combined with circuit open, stay unhealthy.
	}

	// Bulkhead — Degraded (only if not already Critical)
	if p.bh != nil && p.bh.Full() {
		if status.Criticality < CriticalityDegraded {
			status.Criticality = CriticalityDegraded
		}
		if status.Healthy && status.State == "healthy" {
			status.State = "bulkhead_full"
		}
	}

	// Stale cache — flag only, no criticality impact
	if p.sc != nil && p.sc.ServingStale() {
		status.ServingStale = true
		status.StaleAge = p.sc.StaleAge()
	}

	// Dependencies — propagate health from sub-dependencies
	for _, dep := range p.deps {
		depStatus := dep.HealthStatus()
		status.Dependencies = append(status.Dependencies, depStatus)

		// If a dependency is critical → this policy becomes degraded (at minimum)
		if depStatus.Criticality == CriticalityCritical && !depStatus.Healthy {
			if status.Criticality < CriticalityDegraded {
				status.Criticality = CriticalityDegraded
			}
		}
	}

	return status
}
