package r8eotel_test

import (
	"context"
	"errors"
	"testing"
	"time"

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

// collectFloat64 returns the value of the first data point of the named
// Float64 gauge in rm.
func collectFloat64(t *testing.T, rm metricdata.ResourceMetrics, name string) float64 {
	t.Helper()

	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}

			gauge, ok := metric.Data.(metricdata.Gauge[float64])
			require.True(t, ok, "metric %q is not a Float64 gauge", name)
			require.Len(t, gauge.DataPoints, 1)

			return gauge.DataPoints[0].Value
		}
	}

	t.Fatalf("metric %q not found", name)

	return 0
}

// scriptedLatencyClock returns a pre-set elapsed per Do() call (Since is invoked
// exactly once per Do), so a test can record a known latency DISTRIBUTION into
// the window and assert that each percentile gauge reports its own distinct
// value — pinning the p50/p95/p99 field mapping, not just the seconds conversion.
type scriptedLatencyClock struct {
	elapsed []time.Duration
	idx     int
}

func (*scriptedLatencyClock) Now() time.Time { return time.Unix(1_700_000_000, 0) }

func (c *scriptedLatencyClock) Since(time.Time) time.Duration {
	d := c.elapsed[c.idx%len(c.elapsed)]
	c.idx++

	return d
}

func (*scriptedLatencyClock) NewTimer(time.Duration) r8e.Timer { return stoppedTimer{} }

type stoppedTimer struct{}

func (stoppedTimer) C() <-chan time.Time      { return nil }
func (stoppedTimer) Stop() bool               { return true }
func (stoppedTimer) Reset(time.Duration) bool { return false }

// TestRegisterReportsLatencyPercentiles proves the latency gauges report the
// window percentiles converted to seconds, with each gauge wired to its own
// percentile (a distribution with distinct p50/p95/p99 catches a field swap).
func TestRegisterReportsLatencyPercentiles(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("test")

	// 100 samples by nearest rank: p50=10ms, p95=100ms, p99=200ms — three
	// distinct expected gauge values.
	elapsed := make([]time.Duration, 0, 100)
	for range 50 {
		elapsed = append(elapsed, 10*time.Millisecond)
	}

	for range 45 {
		elapsed = append(elapsed, 100*time.Millisecond)
	}

	for range 5 {
		elapsed = append(elapsed, 200*time.Millisecond)
	}

	reg := r8e.NewRegistry()
	policy := r8e.NewPolicy[string]("svc",
		r8e.WithRegistry(reg),
		r8e.WithClock(&scriptedLatencyClock{elapsed: elapsed}),
	)

	registration, err := r8eotel.Register(meter, reg)
	require.NoError(t, err)

	defer func() { require.NoError(t, registration.Unregister()) }()

	for range len(elapsed) {
		_, err := policy.Do(context.Background(), func(context.Context) (string, error) {
			return "ok", nil
		})
		require.NoError(t, err)
	}

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	p50 := collectFloat64(t, rm, "r8e.policy.latency_p50")
	p95 := collectFloat64(t, rm, "r8e.policy.latency_p95")
	p99 := collectFloat64(t, rm, "r8e.policy.latency_p99")

	// Distinct expected values (0.01s / 0.1s / 0.2s) within DDSketch's ~2%
	// relative error: a gauge wired to the wrong field would fail its assertion.
	assert.InEpsilon(t, 0.01, p50, 0.02)
	assert.InEpsilon(t, 0.1, p95, 0.02)
	assert.InEpsilon(t, 0.2, p99, 0.02)
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

	gotNames := make([]string, 0)
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			gotNames = append(gotNames, metric.Name)
		}
	}

	// The full set of instruments Register emits. ElementsMatch (not Contains)
	// so a dropped OR an added-but-untested instrument both fail the guard.
	want := []string{
		// Counters.
		"r8e.policy.retries", "r8e.policy.timeouts",
		"r8e.policy.circuit_opens", "r8e.policy.circuit_closes",
		"r8e.policy.circuit_half_opens", "r8e.policy.rate_limited",
		"r8e.policy.bulkhead_rejected", "r8e.policy.bulkhead_timeouts",
		"r8e.policy.hedges_triggered", "r8e.policy.hedges_won",
		"r8e.policy.fallbacks_used", "r8e.policy.retry_budget_exceeded",
		"r8e.policy.time_budget_exceeded", "r8e.policy.coalesce_leaders",
		"r8e.policy.coalesce_followers", "r8e.policy.concurrency_rejected",
		"r8e.policy.throttled", "r8e.policy.rate_adaptations",
		"r8e.policy.slow_call_rate_exceeded",
		"r8e.policy.cache_hits", "r8e.policy.cache_misses",
		"r8e.policy.cache_stores", "r8e.policy.cache_stale_served",
		"r8e.policy.cache_refreshes",
		"r8e.policy.panics_recovered",
		"r8e.policy.concurrency_budget_exceeded",
		"r8e.policy.chaos_injected",
		// Gauges.
		"r8e.policy.bulkhead_in_use", "r8e.policy.bulkhead_capacity",
		"r8e.policy.bulkhead_queued", "r8e.policy.circuit_state",
		"r8e.policy.healthy", "r8e.policy.saturated",
		"r8e.policy.coalesce_in_flight", "r8e.policy.concurrency_limit",
		"r8e.policy.concurrency_in_flight", "r8e.policy.retry_budget_tokens",
		"r8e.policy.throttle_probability", "r8e.policy.rate_limit",
		"r8e.policy.slow_call_rate",
		"r8e.policy.concurrency_budget_in_use",
		"r8e.policy.latency_p50", "r8e.policy.latency_p95",
		"r8e.policy.latency_p99", "r8e.policy.adaptive_timeout",
		"r8e.policy.adaptive_hedge_delay",
	}
	assert.ElementsMatch(t, want, gotNames,
		"registered instruments drifted from the expected set")
}
