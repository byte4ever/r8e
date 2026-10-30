package r8e

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uncomparableReporter is a HealthReporter whose dynamic type cannot be
// compared with ==: a slice field makes == panic on two such values.
type uncomparableReporter struct {
	tags []string
}

func (uncomparableReporter) Name() string { return "uncomparable" }

func (uncomparableReporter) HealthStatus() PolicyStatus {
	return PolicyStatus{Name: "uncomparable", Healthy: true}
}

func policyNames(reg *Registry) []string {
	var names []string
	for _, ps := range reg.Health().Policies {
		names = append(names, ps.Name)
	}

	return names
}

// ---------------------------------------------------------------------------
// Registry.Unregister
// ---------------------------------------------------------------------------

func TestRegistryUnregisterRemovesThePolicyFromEveryReader(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	keep := NewPolicy[string]("keep", WithRegistry(reg))
	drop := NewPolicy[string]("drop", WithRegistry(reg))

	require.True(t, reg.Unregister(drop))

	assert.Equal(t, []string{"keep"}, policyNames(reg))

	readiness := reg.CheckReadiness()
	require.Len(t, readiness.Policies, 1)
	assert.Equal(t, "keep", readiness.Policies[0].Name)

	snapshot := reg.Snapshot()
	require.Len(t, snapshot, 1)
	assert.Equal(t, keep.Metrics().Name, snapshot[0].Name)

	assert.False(t, reg.Unregister(drop), "already removed")
}

func TestRegistryUnregisterMatchesByIdentity(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	first := NewPolicy[string]("same-name", WithRegistry(reg))
	second := NewPolicy[string]("same-name", WithRegistry(reg))

	require.True(t, reg.Unregister(first))
	assert.False(t, reg.Unregister(first), "the same-name policy left is not the one removed")

	require.Len(t, reg.Health().Policies, 1, "the other policy of the same name stays")
	require.True(t, reg.Unregister(second))
	assert.Empty(t, reg.Health().Policies)
}

// staticReporter is a func-free, comparable HealthReporter: two pointers to
// equal values are still two reporters.
type staticReporter struct {
	name string
}

func (r *staticReporter) Name() string { return r.name }

func (r *staticReporter) HealthStatus() PolicyStatus {
	return PolicyStatus{Name: r.name, Healthy: true}
}

func TestRegistryUnregisterKeepsAnEqualReporterAtAnotherAddress(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	first := &staticReporter{name: "db"}
	second := &staticReporter{name: "db"}
	reg.Register(first)
	reg.Register(second)

	require.True(t, reg.Unregister(first))
	require.Equal(t, []string{"db"}, policyNames(reg))
	assert.Same(t, second, (*reg.reporters.Load())[0])
}

// valueReporter is a value-type HealthReporter whose only field is an
// interface: == compares the values it holds.
type valueReporter struct {
	v any
}

func (valueReporter) Name() string { return "value" }

func (valueReporter) HealthStatus() PolicyStatus {
	return PolicyStatus{Name: "value", Healthy: true}
}

func TestRegistryUnregisterMatchesAValueReporterByValue(t *testing.T) {
	t.Parallel()

	t.Run("comparable value", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		reg.Register(valueReporter{v: "x"})

		assert.True(t, reg.Unregister(valueReporter{v: "x"}))
		assert.Empty(t, reg.Health().Policies)
	})

	t.Run("uncomparable value in the interface field", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		reg.Register(valueReporter{v: []string{"a"}})

		assert.NotPanics(t, func() {
			assert.False(t, reg.Unregister(valueReporter{v: []string{"a"}}))
		})
		assert.Len(t, reg.Health().Policies, 1)
	})
}

func TestRegistryUnregisterRemovesEveryRegistration(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	p := NewPolicy[string]("twice", WithRegistry(reg))
	reg.Register(p)

	require.Len(t, reg.Health().Policies, 2)
	require.True(t, reg.Unregister(p))
	assert.Empty(t, reg.Health().Policies)
}

func TestRegistryUnregisterNeverMatchesAnUncomparableReporter(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	reg.Register(uncomparableReporter{tags: []string{"a"}})

	assert.NotPanics(t, func() {
		assert.False(t, reg.Unregister(uncomparableReporter{tags: []string{"a"}}))
	})
	assert.Len(t, reg.Health().Policies, 1)

	assert.False(t, reg.Unregister(nil))
}

func TestRegistryUnregisterFromTheDefaultRegistry(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string]("unregister-default-registry-test")

	require.Contains(t, policyNames(DefaultRegistry()), "unregister-default-registry-test")
	require.True(t, DefaultRegistry().Unregister(p))
	assert.NotContains(t, policyNames(DefaultRegistry()), "unregister-default-registry-test")
}

func TestRegistryConcurrentUnregisterAndRead(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()

	policies := make([]*Policy[int], 10)
	for i := range policies {
		policies[i] = NewPolicy[int]("concurrent-unreg", WithRegistry(reg))
	}

	var wg sync.WaitGroup

	for range 20 {
		wg.Go(func() {
			for range 100 {
				for _, ps := range reg.Health().Policies {
					assert.Equal(t, "concurrent-unreg", ps.Name)
				}

				_ = reg.CheckReadiness()
				_ = reg.Snapshot()
			}
		})
	}

	for _, p := range policies {
		wg.Go(func() { assert.True(t, reg.Unregister(p)) })
	}

	wg.Wait()

	assert.Empty(t, reg.Health().Policies)
}

func TestRegistryConcurrentUnregisterAndRegister(t *testing.T) {
	t.Parallel()

	const writers = 200

	reg := NewRegistry()

	old := make([]*Policy[int], writers)
	for i := range old {
		old[i] = NewPolicy[int]("old", WithRegistry(reg))
	}

	var (
		wg      sync.WaitGroup
		removed = make([]bool, writers)
		fresh   = make([]*Policy[int], writers)
		start   = make(chan struct{})
	)

	for i := range writers {
		wg.Go(func() {
			<-start

			removed[i] = reg.Unregister(old[i])
		})
		wg.Go(func() {
			<-start

			fresh[i] = NewPolicy[int]("new", WithRegistry(reg))
		})
	}

	close(start)
	wg.Wait()

	for i, ok := range removed {
		assert.True(t, ok, "Unregister %d", i)
	}

	// Exactly the policies registered during the race are left, once each.
	seen := make(map[HealthReporter]int)
	for _, hr := range *reg.reporters.Load() {
		seen[hr]++
	}

	require.Len(t, seen, writers)

	for i, p := range fresh {
		assert.Equal(t, 1, seen[p], "fresh policy %d", i)
	}
}

// ---------------------------------------------------------------------------
// Policy.Registry
// ---------------------------------------------------------------------------

func TestPolicyRegistryReturnsTheRegistryItJoined(t *testing.T) {
	t.Parallel()

	own := NewRegistry()

	tests := []struct {
		want   *Registry
		policy *Policy[string]
		name   string
	}{
		{
			name:   "WithRegistry",
			policy: NewPolicy[string]("policy-registry-own", WithRegistry(own)),
			want:   own,
		},
		{
			name:   "default registry",
			policy: NewPolicy[string]("policy-registry-default"),
			want:   DefaultRegistry(),
		},
		{
			name:   "unnamed policy",
			policy: NewPolicy[string](""),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Same(t, tt.want, tt.policy.Registry())
			assert.Equal(t, tt.want != nil, tt.policy.Registry().Unregister(tt.policy))
		})
	}
}

func TestPolicyRegistryUnregisterIdiomLeavesItsOwnRegistry(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	policy := NewPolicy[string]("policy-registry-idiom", WithRegistry(reg))
	require.Contains(t, policyNames(reg), "policy-registry-idiom")

	func() {
		defer policy.Registry().Unregister(policy)
	}()

	assert.NotContains(t, policyNames(reg), "policy-registry-idiom")
}

func TestNilRegistryUnregisterReturnsFalse(t *testing.T) {
	t.Parallel()

	var reg *Registry

	assert.NotPanics(t, func() {
		assert.False(t, reg.Unregister(NewPolicy[string]("")))
	})
}
