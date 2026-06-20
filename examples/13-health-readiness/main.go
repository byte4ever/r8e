// Example 13-health-readiness: Demonstrates HealthReporter, DependsOn,
// WithReadinessImpact, and exposing /readyz (gates traffic) and /healthz
// (informational, always 200) endpoints.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/byte4ever/r8e/r8ehttp"
)

func main() {
	ctx := context.Background()
	reg := r8e.NewRegistry()

	// Create two policies with a circuit breaker, registered in the same
	// registry.
	dbPolicy := r8e.NewPolicy[string](
		"database",
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(2),
			r8e.RecoveryTimeout(30*time.Second),
		),
		// The pod cannot serve without the database, so gate readiness on it.
		// Without WithReadinessImpact a policy's health is reported but never
		// removes the pod from rotation.
		r8e.WithReadinessImpact(),
		r8e.WithRegistry(reg),
	)
	apiPolicy := r8e.NewPolicy[string](
		"api-gateway",
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(3),
			r8e.RecoveryTimeout(30*time.Second),
		),
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
		//nolint:errcheck // example program — intentionally triggering failures
		_, _ = dbPolicy.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("connection refused")
		})
	}

	printHealth(dbPolicy)
	printHealth(apiPolicy)
	printReadiness(reg)

	// --- HTTP readiness endpoint ---
	fmt.Println("\n=== HTTP /readyz Endpoint ===")

	handler := r8ehttp.ReadinessHandler(reg)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", http.NoBody))
	fmt.Printf("  HTTP %d\n", rec.Code)

	var body map[string]any

	//nolint:errcheck // example program
	_ = json.Unmarshal(rec.Body.Bytes(), &body)

	pretty, _ := json.MarshalIndent( //nolint:errcheck // example program
		body,
		"  ",
		"  ",
	)
	fmt.Printf("  %s\n", pretty)

	// --- HTTP health endpoint (always 200; never gates traffic) ---
	fmt.Println("\n=== HTTP /healthz Endpoint ===")

	healthRec := httptest.NewRecorder()
	r8ehttp.HealthHandler(reg).
		ServeHTTP(healthRec, httptest.NewRequest("GET", "/healthz", http.NoBody))
	fmt.Printf("  HTTP %d\n", healthRec.Code)

	var health map[string]any

	//nolint:errcheck // example program
	_ = json.Unmarshal(healthRec.Body.Bytes(), &health)
	fmt.Printf("  status: %v\n", health["status"])

	_ = apiPolicy // keep compiler happy
}

func printHealth(hr r8e.HealthReporter) {
	status := hr.HealthStatus()
	fmt.Printf("  %s: healthy=%v, state=%s, criticality=%v\n",
		status.Name, status.Healthy, status.State, status.Criticality)

	for _, dep := range status.Dependencies {
		fmt.Printf(
			"    dep %s: healthy=%v, state=%s\n",
			dep.Name,
			dep.Healthy,
			dep.State,
		)
	}
}

func printReadiness(reg *r8e.Registry) {
	status := reg.CheckReadiness()
	fmt.Printf("  Readiness: ready=%v\n", status.Ready)
}
