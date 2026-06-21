package r8eotel_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8eotel"
)

// collectInt64 returns the value and "policy" attribute of the first data point
// of the named instrument (gauge or sum) in rm.
func collectInt64(t *testing.T, rm metricdata.ResourceMetrics, name string) (int64, string) {
	t.Helper()

	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}

			switch data := metric.Data.(type) {
			case metricdata.Gauge[int64]:
				return dataPoint(t, data.DataPoints)
			case metricdata.Sum[int64]:
				return dataPoint(t, data.DataPoints)
			default:
				t.Fatalf("metric %q has unexpected data type %T", name, metric.Data)
			}
		}
	}

	t.Fatalf("metric %q not found", name)

	return 0, ""
}

func dataPoint(t *testing.T, points []metricdata.DataPoint[int64]) (int64, string) {
	t.Helper()

	require.Len(t, points, 1)

	policy, ok := points[0].Attributes.Value(attribute.Key("policy"))
	require.True(t, ok, "data point missing policy attribute")

	return points[0].Value, policy.AsString()
}

func TestRegisterReportsPolicyMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")

	reg := r8e.NewRegistry()
	policy := r8e.NewPolicy[string]("svc",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(r8e.FailureThreshold(1)),
		r8e.WithBulkhead(8),
		r8e.WithFallback("fb"),
	)

	registration, err := r8eotel.Register(meter, reg)
	require.NoError(t, err)

	defer func() { require.NoError(t, registration.Unregister()) }()

	// Drive a failure so the breaker opens and a fallback is recorded.
	_, _ = policy.Do(context.Background(), func(context.Context) (string, error) {
		return "", errors.New("down")
	})

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	capacity, policyAttr := collectInt64(t, rm, "r8e.policy.bulkhead_capacity")
	assert.Equal(t, int64(8), capacity)
	assert.Equal(t, "svc", policyAttr)

	state, _ := collectInt64(t, rm, "r8e.policy.circuit_state")
	assert.Equal(t, int64(2), state, "circuit should be open after a failure")

	opens, _ := collectInt64(t, rm, "r8e.policy.circuit_opens")
	assert.Equal(t, int64(1), opens)

	fallbacks, _ := collectInt64(t, rm, "r8e.policy.fallbacks_used")
	assert.Equal(t, int64(1), fallbacks)

	healthy, _ := collectInt64(t, rm, "r8e.policy.healthy")
	assert.Equal(t, int64(0), healthy, "open critical breaker => unhealthy")
}

// TestRegisterEmitsAllInstruments guards every instrument name: a typo or a
// dropped instrument would make the corresponding metric disappear.
func TestRegisterEmitsAllInstruments(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")

	reg := r8e.NewRegistry()
	_ = r8e.NewPolicy[string]("svc",
		r8e.WithRegistry(reg),
		r8e.WithCircuitBreaker(),
		r8e.WithRateLimit(10),
		r8e.WithBulkhead(4),
	)

	registration, err := r8eotel.Register(meter, reg)
	require.NoError(t, err)

	defer func() { require.NoError(t, registration.Unregister()) }()

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	got := make(map[string]bool)
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			got[metric.Name] = true
		}
	}

	want := []string{
		"r8e.policy.retries", "r8e.policy.timeouts",
		"r8e.policy.circuit_opens", "r8e.policy.circuit_closes",
		"r8e.policy.circuit_half_opens", "r8e.policy.rate_limited",
		"r8e.policy.bulkhead_rejected", "r8e.policy.hedges_triggered",
		"r8e.policy.hedges_won", "r8e.policy.fallbacks_used",
		"r8e.policy.retry_budget_exceeded",
		"r8e.policy.coalesce_leaders", "r8e.policy.coalesce_followers",
		"r8e.policy.concurrency_rejected",
		"r8e.policy.bulkhead_in_use", "r8e.policy.bulkhead_capacity",
		"r8e.policy.circuit_state", "r8e.policy.healthy",
		"r8e.policy.saturated", "r8e.policy.retry_budget_tokens",
		"r8e.policy.coalesce_in_flight",
		"r8e.policy.concurrency_limit", "r8e.policy.concurrency_in_flight",
	}
	for _, name := range want {
		assert.Contains(t, got, name)
	}
}
