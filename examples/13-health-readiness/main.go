// Example 13-health-readiness: Demonstrates HealthReporter, DependsOn,
// and exposing an HTTP /readyz endpoint.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()
	reg := r8e.NewRegistry()

	// Create two policies with a circuit breaker, registered in the same registry.
	dbPolicy := r8e.NewPolicy[string]("database",
		r8e.WithCircuitBreaker(r8e.FailureThreshold(2), r8e.RecoveryTimeout(30*time.Second)),
		r8e.WithRegistry(reg),
	)
	apiPolicy := r8e.NewPolicy[string]("api-gateway",
		r8e.WithCircuitBreaker(r8e.FailureThreshold(3), r8e.RecoveryTimeout(30*time.Second)),
		r8e.DependsOn(dbPolicy), // api-gateway depends on database
		r8e.WithRegistry(reg),
	)

	// --- Health status when everything is healthy ---
	fmt.Println("=== Initial Health Status ===")
	printHealth(dbPolicy)
	printHealth(apiPolicy)
	printReadiness(reg)

	// --- Simulate database failures to open the circuit ---
	fmt.Println("\n=== Triggering database circuit breaker ===")
	for range 3 {
		dbPolicy.Do(ctx, func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("connection refused")
		})
	}

	printHealth(dbPolicy)
	printHealth(apiPolicy)
	printReadiness(reg)

	// --- HTTP readiness endpoint ---
	fmt.Println("\n=== HTTP /readyz Endpoint ===")
	handler := r8e.ReadinessHandler(reg)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
	fmt.Printf("  HTTP %d\n", rec.Code)
	var body map[string]any
	json.Unmarshal(rec.Body.Bytes(), &body)
	pretty, _ := json.MarshalIndent(body, "  ", "  ")
	fmt.Printf("  %s\n", pretty)

	_ = apiPolicy // keep compiler happy
}

func printHealth(hr r8e.HealthReporter) {
	status := hr.HealthStatus()
	fmt.Printf("  %s: healthy=%v, state=%s, criticality=%v\n",
		status.Name, status.Healthy, status.State, status.Criticality)
	for _, dep := range status.Dependencies {
		fmt.Printf("    dep %s: healthy=%v, state=%s\n", dep.Name, dep.Healthy, dep.State)
	}
}

func printReadiness(reg *r8e.Registry) {
	status := reg.CheckReadiness()
	fmt.Printf("  Readiness: ready=%v\n", status.Ready)
}
