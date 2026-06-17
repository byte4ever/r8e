package r8econf

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
)

func bulkheadCap(t *testing.T, store *Store, name string) int64 {
	t.Helper()

	for _, m := range store.Registry().Snapshot() {
		if m.Name == name {
			return m.BulkheadCap
		}
	}

	t.Fatalf("policy %q not found in snapshot", name)

	return 0
}

func TestStoreReloadRetunesLivePolicy(t *testing.T) {
	store, err := Load(writeTempFile(t, `{"policies":{"p":{"bulkhead":2}}}`))
	require.NoError(t, err)

	_, err = GetPolicy[string](store, "p")
	require.NoError(t, err)
	require.Equal(t, int64(2), bulkheadCap(t, store, "p"))

	// Reload with a larger bulkhead; the live policy must be retuned in place.
	require.NoError(t, store.Reload(writeTempFile(t, `{"policies":{"p":{"bulkhead":10}}}`)))
	assert.Equal(t, int64(10), bulkheadCap(t, store, "p"))
}

func TestStoreReloadSkipsUnbuiltPolicies(t *testing.T) {
	store, err := Load(writeTempFile(t, `{"policies":{"p":{"bulkhead":2}}}`))
	require.NoError(t, err)

	// "p" was never built via GetPolicy, so Reload should not error on it.
	require.NoError(t, store.Reload(writeTempFile(t, `{"policies":{"p":{"bulkhead":10}}}`)))

	// A subsequent GetPolicy picks up the reloaded config.
	_, err = GetPolicy[string](store, "p")
	require.NoError(t, err)
	assert.Equal(t, int64(10), bulkheadCap(t, store, "p"))
}

func TestStoreReloadInvalidFile(t *testing.T) {
	store, err := Load(writeTempFile(t, `{"policies":{"p":{"bulkhead":2}}}`))
	require.NoError(t, err)

	require.Error(t, store.Reload("../testdata/nonexistent.json"))
}

func TestStoreReloadStructuralChangeErrors(t *testing.T) {
	store, err := Load(writeTempFile(t, `{"policies":{"p":{"bulkhead":2}}}`))
	require.NoError(t, err)

	_, err = GetPolicy[string](store, "p")
	require.NoError(t, err)

	// The live "p" has no circuit breaker; adding one cannot be hot-reloaded.
	err = store.Reload(writeTempFile(t,
		`{"policies":{"p":{"bulkhead":10,"circuit_breaker":{"failure_threshold":3}}}}`))
	require.ErrorIs(t, err, r8e.ErrPatternAbsent)
}
