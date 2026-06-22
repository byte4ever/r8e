// Example 01-metrics: export r8e's per-policy counters and gauges as
// OpenTelemetry observable instruments. Register wires every policy in a registry
// to a meter; here a manual reader pulls a snapshot on demand so we can print a
// few instruments. A real service swaps the manual reader for a PeriodicReader
// plus an OTLP or Prometheus exporter.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8eotel"
)

func main() {
	ctx := context.Background()

	reader := sdkmetric.NewManualReader()
	meter := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader)).Meter("example")

	// Register r8e's metrics, sourced from the default registry that every named
	// policy auto-joins. Unregister stops the reporting callback.
	registration, err := r8eotel.Register(meter, r8e.DefaultRegistry())
	if err != nil {
		panic(err)
	}

	defer registration.Unregister() //nolint:errcheck // example cleanup

	policy := r8e.NewPolicy[string]("payments",
		r8e.WithRetry(3, r8e.ConstantBackoff(0)),
		r8e.WithCircuitBreaker(r8e.FailureThreshold(2)),
	)

	// Drive a few failing calls so the counters move and the breaker trips.
	for range 5 {
		//nolint:errcheck // example: the failure is the point
		_, _ = policy.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("downstream down")
		})
	}

	// Pull a snapshot and print a few instruments.
	var rm metricdata.ResourceMetrics
	if collectErr := reader.Collect(ctx, &rm); collectErr != nil {
		panic(collectErr)
	}

	fmt.Println("=== r8e metrics via OpenTelemetry ===")

	for _, name := range []string{
		"r8e.policy.retries",
		"r8e.policy.circuit_opens",
		"r8e.policy.circuit_state", // 0=closed 1=half-open 2=open 3=ramping
	} {
		value, policyName := readInt64(rm, name)
		fmt.Printf("  %-26s = %d  (policy=%s)\n", name, value, policyName)
	}
}

// readInt64 returns the first data point value and "policy" attribute of the
// named int64 instrument (gauge or sum) in rm.
func readInt64(rm metricdata.ResourceMetrics, name string) (value int64, policyName string) {
	for _, scope := range rm.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != name {
				continue
			}

			var points []metricdata.DataPoint[int64]

			switch data := metric.Data.(type) {
			case metricdata.Gauge[int64]:
				points = data.DataPoints
			case metricdata.Sum[int64]:
				points = data.DataPoints
			}

			if len(points) == 0 {
				return 0, ""
			}

			policy, _ := points[0].Attributes.Value("policy")

			return points[0].Value, policy.AsString()
		}
	}

	return 0, "(not found)"
}
