package r8e

import (
	"sync"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// ReadinessStatus — result of checking all registered policies
// ---------------------------------------------------------------------------

// ReadinessStatus is the result of checking all registered policies.
type ReadinessStatus struct {
	Ready    bool           `json:"ready"`
	Policies []PolicyStatus `json:"policies"`
}

// ---------------------------------------------------------------------------
// Registry — tracks HealthReporter instances and derives readiness status
// ---------------------------------------------------------------------------

// Registry tracks HealthReporter instances and derives readiness status.
//
// Pattern: Singleton — DefaultRegistry uses sync.Once for safe lazy init;
// explicit registries can be created for testing or multi-tenant scenarios.
type Registry struct {
	mu        sync.Mutex
	reporters atomic.Pointer[[]HealthReporter]
	configs   map[string]policyConfig // stored by LoadConfig, read by GetPolicy
}

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	r := &Registry{}
	empty := make([]HealthReporter, 0)
	r.reporters.Store(&empty)
	return r
}

// Register adds a HealthReporter to the registry.
// This is typically called during startup by NewPolicy.
// It is safe for concurrent use but intended for initialization only.
func (r *Registry) Register(hr HealthReporter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	old := *r.reporters.Load()
	// Create a new slice (copy-on-write) to avoid mutating the slice
	// that concurrent readers may be iterating.
	updated := make([]HealthReporter, len(old), len(old)+1)
	copy(updated, old)
	updated = append(updated, hr)
	r.reporters.Store(&updated)
}

// CheckReadiness iterates all registered reporters and builds a ReadinessStatus.
// Ready is false if any policy has CriticalityCritical and is unhealthy.
func (r *Registry) CheckReadiness() ReadinessStatus {
	reporters := *r.reporters.Load()

	status := ReadinessStatus{
		Ready:    true,
		Policies: make([]PolicyStatus, 0, len(reporters)),
	}

	for _, hr := range reporters {
		ps := hr.HealthStatus()
		status.Policies = append(status.Policies, ps)

		// A critical unhealthy policy makes the service not ready.
		if ps.Criticality == CriticalityCritical && !ps.Healthy {
			status.Ready = false
		}
	}

	return status
}

// ---------------------------------------------------------------------------
// DefaultRegistry — package-level global registry singleton
// ---------------------------------------------------------------------------

// defaultRegistry is the package-level global registry.
var (
	defaultRegistryOnce sync.Once
	defaultRegistryVal  *Registry
)

// DefaultRegistry returns the package-level global registry, creating it
// on first call.
//
// Pattern: Singleton — lazy initialization via sync.Once ensures exactly one
// global registry exists and is safe for concurrent access.
func DefaultRegistry() *Registry {
	defaultRegistryOnce.Do(func() {
		defaultRegistryVal = NewRegistry()
	})
	return defaultRegistryVal
}
