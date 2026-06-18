package r8ehttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8ehttp"
)

// TestReadinessHandlerAllHealthy verifies that when all registered policies
// are healthy the handler returns 200 OK with Ready=true.
func TestReadinessHandlerAllHealthy(t *testing.T) {
	t.Parallel()

	reg := r8e.NewRegistry()

	_ = r8e.NewPolicy[string]("api-1",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(),
	)

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var status r8e.ReadinessStatus
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&status))
	require.True(t, status.Ready)
	require.Len(t, status.Policies, 1)
	assert.Equal(t, "api-1", status.Policies[0].Name)
}

// TestReadinessHandlerOneCritical verifies that when one policy has an open
// circuit breaker the handler returns 503 with Ready=false.
func TestReadinessHandlerOneCritical(t *testing.T) {
	t.Parallel()

	reg := r8e.NewRegistry()

	policy := r8e.NewPolicy[string]("api-down",
		r8e.WithRegistry(reg),
		r8e.WithReadinessImpact(),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(2),
			r8e.RecoveryTimeout(time.Hour),
		),
	)

	ctx := context.Background()
	for range 2 {
		_, _ = policy.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("fail")
		})
	}

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	var status r8e.ReadinessStatus
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&status))
	require.False(t, status.Ready)
}

// TestReadinessHandlerEmptyRegistry verifies that an empty registry
// yields 200 OK with Ready=true and an empty Policies slice.
func TestReadinessHandlerEmptyRegistry(t *testing.T) {
	t.Parallel()

	reg := r8e.NewRegistry()

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var status r8e.ReadinessStatus
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&status))
	require.True(t, status.Ready)
	require.Empty(t, status.Policies)
}

// TestReadinessHandlerJSONStructure verifies the JSON body contains the
// expected fields: name, healthy, criticality, and state.
func TestReadinessHandlerJSONStructure(t *testing.T) {
	t.Parallel()

	reg := r8e.NewRegistry()

	_ = r8e.NewPolicy[string]("svc-a",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(),
	)

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))

	for _, key := range []string{"ready", "policies"} {
		assert.Contains(t, raw, key, "missing top-level key %q", key)
	}

	var policies []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw["policies"], &policies))
	require.NotEmpty(t, policies)
	for _, key := range []string{"name", "healthy", "criticality", "state", "affects_readiness"} {
		assert.Contains(t, policies[0], key, "missing policy key %q", key)
	}
}

// TestReadinessHandlerContentType verifies the Content-Type header is
// application/json.
func TestReadinessHandlerContentType(t *testing.T) {
	t.Parallel()

	reg := r8e.NewRegistry()

	handler := r8ehttp.ReadinessHandler(reg)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
}

// TestReadinessHandlerServerEndToEnd drives the handler through a real HTTP
// server: healthy → 200, then an opened circuit → 503.
func TestReadinessHandlerServerEndToEnd(t *testing.T) {
	t.Parallel()

	reg := r8e.NewRegistry()

	healthy := r8e.NewPolicy[string]("healthy-service",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(5),
			r8e.RecoveryTimeout(time.Hour),
		),
	)
	unhealthy := r8e.NewPolicy[string]("unhealthy-service",
		r8e.WithRegistry(reg),
		r8e.WithReadinessImpact(),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(2),
			r8e.RecoveryTimeout(time.Hour),
		),
	)

	srv := httptest.NewServer(r8ehttp.ReadinessHandler(reg))
	defer srv.Close()

	ctx := context.Background()

	resp, err := srv.Client().Get(srv.URL)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	for range 2 {
		_, _ = unhealthy.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("failure")
		})
	}
	_, _ = healthy.Do(ctx, func(_ context.Context) (string, error) {
		return "ok", nil
	})

	resp2, err := srv.Client().Get(srv.URL)
	require.NoError(t, err)
	defer func() { _ = resp2.Body.Close() }()

	require.Equal(t, http.StatusServiceUnavailable, resp2.StatusCode)

	var status r8e.ReadinessStatus
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&status))
	assert.False(t, status.Ready, "Ready should be false when a circuit is open")
}

// BenchmarkReadinessHandler benchmarks the readiness handler with a single
// registered policy.
func BenchmarkReadinessHandler(b *testing.B) {
	reg := r8e.NewRegistry()

	_ = r8e.NewPolicy[string]("bench-policy",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(),
	)

	handler := r8ehttp.ReadinessHandler(reg)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}
}
