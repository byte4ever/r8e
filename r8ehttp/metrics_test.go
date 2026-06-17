package r8ehttp_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8ehttp"
)

func TestMetricsHandler(t *testing.T) {
	t.Parallel()

	reg := r8e.NewRegistry()
	_ = r8e.NewPolicy[string]("svc",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(),
		r8e.WithBulkhead(8),
	)

	rec := httptest.NewRecorder()
	r8ehttp.MetricsHandler(reg).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))

	var metrics []r8e.PolicyMetrics
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&metrics))
	require.Len(t, metrics, 1)
	assert.Equal(t, "svc", metrics[0].Name)
	assert.Equal(t, "closed", metrics[0].CircuitState)
	assert.Equal(t, int64(8), metrics[0].BulkheadCap)
}

func TestMetricsHandlerEmpty(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	r8ehttp.MetricsHandler(r8e.NewRegistry()).ServeHTTP(
		rec,
		httptest.NewRequest(http.MethodGet, "/metrics", nil),
	)

	require.Equal(t, http.StatusOK, rec.Code)

	var metrics []r8e.PolicyMetrics
	require.NoError(t, json.NewDecoder(rec.Body).Decode(&metrics))
	assert.Empty(t, metrics)
}
