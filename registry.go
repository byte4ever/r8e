package r8e

import (
	"reflect"
	"slices"
	"sync"
	"sync/atomic"
)

// ---------------------------------------------------------------------------
// ReadinessStatus — result of checking all registered policies
// ---------------------------------------------------------------------------.

type (
	// ReadinessStatus is the result of checking all registered policies.
	ReadinessStatus struct {
		Policies []PolicyStatus `json:"policies"`
		Ready    bool           `json:"ready"`
	}

	// HealthReport is the aggregate health of all registered policies. Unlike
	// [ReadinessStatus] it never gates traffic — expose it on an informational
	// endpoint (see r8ehttp.HealthHandler), separate from the readiness probe.
	HealthReport struct {
		// Status is the aggregate health across all policies.
		Status   HealthState    `json:"status"`
		Policies []PolicyStatus `json:"policies"`
	}

	// HealthState is the aggregate health level reported by [Registry.Health].
	// It is derived from the worst per-policy [Criticality] across the registry:
	// critical → unhealthy, degraded → degraded, otherwise healthy. This rollup
	// mapping is intentionally separate from the per-condition severity table.
	HealthState string

	// Registry tracks HealthReporter instances and derives readiness status.
	//
	// Pattern: Singleton — DefaultRegistry uses sync.OnceValue for safe lazy
	// init;
	// explicit registries can be created for testing or multi-tenant scenarios.
	Registry struct {
		reporters atomic.Pointer[[]HealthReporter]
		mu        sync.Mutex
	}
)

const (
	// HealthHealthy means every policy is healthy.
	HealthHealthy HealthState = "healthy"
	// HealthDegraded means at least one policy is impaired but none is down.
	HealthDegraded HealthState = "degraded"
	// HealthUnhealthy means at least one policy is critically unhealthy.
	HealthUnhealthy HealthState = "unhealthy"
)

//nolint:gochecknoglobals // singleton via sync.OnceValue
var defaultRegistry = sync.OnceValue(NewRegistry)

// NewRegistry creates an empty registry.
func NewRegistry() *Registry {
	r := &Registry{}

	var empty []HealthReporter

	r.reporters.Store(&empty)

	return r
}

// Register adds a HealthReporter to the registry. NewPolicy calls it each
// time it builds a named policy, at startup or per job, and the registration
// lasts until [Registry.Unregister] removes it. It is safe for concurrent use
// with Unregister and the readers.
func (r *Registry) Register(hr HealthReporter) {
	r.mu.Lock()
	defer r.mu.Unlock()

	old := *r.reporters.Load()
	// Copy-on-write. The capacity MUST equal len(old): a concurrent reader holds
	// the old backing array, so any spare capacity here would let append scribble
	// into the slot a reader is iterating. cap==len forces a fresh allocation on
	// every grow, keeping published snapshots immutable. Do not pre-grow.
	updated := make([]HealthReporter, len(old), len(old)+1)
	copy(updated, old)
	updated = append(updated, hr)
	r.reporters.Store(&updated)
}

// Unregister removes every registration of hr and reports whether there was
// any. It matches with == on the reporter value: pointer identity for a
// [Policy], so another policy that has the same name stays, and value
// equality for a value-type reporter. A reporter whose == could panic is
// never matched, including a comparable type that holds an uncomparable
// value in an interface field. It is safe for concurrent use with
// [Registry.Register] and the readers, which keep iterating the list they
// started with. On a nil *Registry it does nothing and returns false.
//
// To remove a policy from the registry it joined, whichever that is:
//
//	defer policy.Registry().Unregister(policy)
func (r *Registry) Unregister(hr HealthReporter) bool {
	if r == nil || hr == nil || !reflect.ValueOf(hr).Comparable() {
		return false
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	old := *r.reporters.Load()

	// Copy-on-write, as in Register: readers may still hold old.
	kept := make([]HealthReporter, 0, len(old))
	for _, registered := range old {
		if registered != hr {
			kept = append(kept, registered)
		}
	}

	if len(kept) == len(old) {
		return false
	}

	// Publish with cap == len, like Register.
	kept = slices.Clip(kept)
	r.reporters.Store(&kept)

	return true
}

// CheckReadiness iterates all registered reporters and builds a
// ReadinessStatus. Ready is false only when a policy that opted into readiness
// impact (WithReadinessImpact) is critically down — a critically unhealthy
// policy that did not opt in is reported but does not gate traffic.
func (r *Registry) CheckReadiness() ReadinessStatus {
	reporters := *r.reporters.Load()

	status := ReadinessStatus{
		Ready:    true,
		Policies: make([]PolicyStatus, 0, len(reporters)),
	}

	for _, hr := range reporters {
		ps := hr.HealthStatus()
		status.Policies = append(status.Policies, ps)

		// Only a policy that opted into readiness impact (WithReadinessImpact)
		// removes the pod from rotation — a critically unhealthy policy without
		// it is reported but does not gate traffic.
		if ps.AffectsReadiness && ps.criticallyDown() {
			status.Ready = false
		}
	}

	return status
}

// Health returns the aggregate health of all registered policies. It always
// reports the full picture and never gates traffic; wire it to an
// informational endpoint, not the Kubernetes readiness probe.
func (r *Registry) Health() HealthReport {
	reporters := *r.reporters.Load()

	report := HealthReport{
		Policies: make([]PolicyStatus, 0, len(reporters)),
		Status:   HealthHealthy,
	}

	worst := CriticalityNone

	for _, hr := range reporters {
		ps := hr.HealthStatus()
		report.Policies = append(report.Policies, ps)

		if ps.Criticality > worst {
			worst = ps.Criticality
		}
	}

	switch {
	case worst >= CriticalityCritical:
		report.Status = HealthUnhealthy
	case worst >= CriticalityDegraded:
		report.Status = HealthDegraded
	default:
		// Status stays HealthHealthy.
	}

	return report
}

// Snapshot returns a [PolicyMetrics] for every registered policy that exposes
// metrics. It is safe for concurrent use and takes no locks on the read path
// (the reporter list is read via an atomic snapshot).
func (r *Registry) Snapshot() []PolicyMetrics {
	reporters := *r.reporters.Load()

	out := make([]PolicyMetrics, 0, len(reporters))

	for _, hr := range reporters {
		if mr, ok := hr.(MetricsReporter); ok {
			out = append(out, mr.Metrics())
		}
	}

	return out
}

// DefaultRegistry returns the package-level global registry, creating it
// on first call.
//
// Pattern: Singleton — lazy initialization via sync.OnceValue ensures exactly
// one global registry exists and is safe for concurrent access.
func DefaultRegistry() *Registry {
	return defaultRegistry()
}
