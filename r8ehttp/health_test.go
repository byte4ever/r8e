package r8ehttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8ehttp"
)

func TestHealthHandlerAlwaysOK(t *testing.T) {
	t.Parallel()

	reg := r8e.NewRegistry()
	policy := r8e.NewPolicy[string]("svc",
		r8e.WithRegistry(reg),
		// Note: NOT gating readiness — health reporting is independent.
		r8e.WithCircuitBreaker(r8e.FailureThreshold(1), r8e.RecoveryTimeout(time.Hour)),
	)

	// Drive the breaker open: the policy is now unhealthy.
	_, _ = policy.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("boom")
	})

	rec := httptest.NewRecorder()
	r8ehttp.HealthHandler(reg).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
	)

	// Always 200 — the health endpoint never gates traffic.
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var report r8e.HealthReport
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&report))
	assert.Equal(t, r8e.HealthUnhealthy, report.Status)
	require.Len(t, report.Policies, 1)
	assert.Equal(t, "svc", report.Policies[0].Name)
	assert.Contains(t, report.Policies[0].Conditions, r8e.ConditionCircuitOpen)
}

func TestHealthHandlerEmpty(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	r8ehttp.HealthHandler(r8e.NewRegistry()).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
	)

	require.Equal(t, http.StatusOK, rec.Code)

	var report r8e.HealthReport
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&report))
	assert.Equal(t, r8e.HealthHealthy, report.Status)
	assert.Empty(t, report.Policies)
}

// TestHealthHandlerJSONContract pins the wire field names — they are the
// Kubernetes-probe contract authored on the core status types, so a rename
// there must break a test rather than silently break a consumer. The "svc"
// policy is driven into an open-circuit state with a declared dependency so the
// omitempty "conditions" and "dependencies" fields are populated and pinned too.
func TestHealthHandlerJSONContract(t *testing.T) {
	t.Parallel()

	reg := r8e.NewRegistry()
	dep := r8e.NewPolicy[string]("dep", r8e.WithRegistry(reg), r8e.WithCircuitBreaker())
	svc := r8e.NewPolicy[string]("svc",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(r8e.FailureThreshold(1), r8e.RecoveryTimeout(time.Hour)),
		r8e.DependsOn(dep),
	)

	// Open svc's breaker so PolicyStatus.Conditions is non-empty.
	_, _ = svc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("boom")
	})

	rec := httptest.NewRecorder()
	r8ehttp.HealthHandler(reg).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/healthz", nil),
	)

	var top map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &top))
	assert.Contains(t, top, "status")
	assert.Contains(t, top, "policies")

	var policies []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(top["policies"], &policies))

	// Locate the "svc" policy (registration order is not guaranteed).
	var svcFields map[string]json.RawMessage

	for _, p := range policies {
		var name string
		if err := json.Unmarshal(p["name"], &name); err == nil && name == "svc" {
			svcFields = p
		}
	}

	require.NotNil(t, svcFields, "svc policy missing from report")

	for _, key := range []string{
		"name", "state", "criticality", "healthy",
		"affects_readiness", "conditions", "dependencies",
	} {
		assert.Contains(t, svcFields, key, "missing wire field %q", key)
	}
}
