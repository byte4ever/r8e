package r8e

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

	// PolicyStatus represents the current health state of a policy.
	PolicyStatus struct {
		Name         string         `json:"name"`
		State        string         `json:"state"`
		Dependencies []PolicyStatus `json:"dependencies,omitempty"`
		Criticality  Criticality    `json:"criticality"`
		Healthy      bool           `json:"healthy"`
	}
)

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
// HealthStatus on Policy[T]
// ---------------------------------------------------------------------------.

// HealthStatus derives the policy's current health by inspecting stateful
// patterns.
func (p *Policy[T]) HealthStatus() PolicyStatus {
	status := PolicyStatus{
		Name:    p.name,
		Healthy: true,
		State:   "healthy",
	}

	// Circuit breaker — Critical
	if p.circuitBreaker != nil {
		s := p.circuitBreaker.State()

		switch s {
		case "open":
			status.Healthy = false
			status.Criticality = CriticalityCritical
			status.State = "circuit_open"
		case "half_open":
			status.State = "circuit_half_open"
			// half_open is not unhealthy — it's recovering
		default:
			// closed — no action needed
		}
	}

	// Rate limiter — Degraded (only if not already Critical)
	if p.rateLimiter != nil && p.rateLimiter.Saturated() {
		if status.Criticality < CriticalityDegraded {
			status.Criticality = CriticalityDegraded
		}

		if status.Healthy {
			status.State = "rate_limited"
		}
		// Rate limiter saturation alone doesn't make the policy unhealthy
		// (it's degraded, not down). But if combined with circuit open, stay
		// unhealthy.
	}

	// Bulkhead — Degraded (only if not already Critical)
	if p.bulkhead != nil && p.bulkhead.Full() {
		if status.Criticality < CriticalityDegraded {
			status.Criticality = CriticalityDegraded
		}

		if status.Healthy && status.State == "healthy" {
			status.State = "bulkhead_full"
		}
	}

	// Dependencies — propagate health from sub-dependencies
	for _, dep := range p.deps {
		depStatus := dep.HealthStatus()
		status.Dependencies = append(status.Dependencies, depStatus)

		// If a dependency is critical → this policy becomes degraded (at
		// minimum)
		if depStatus.Criticality != CriticalityCritical || depStatus.Healthy {
			continue
		}

		if status.Criticality < CriticalityDegraded {
			status.Criticality = CriticalityDegraded
		}
	}

	return status
}
