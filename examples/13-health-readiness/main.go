// Example 13-health-readiness: Demonstrates HealthReporter, DependsOn,
// WithReadinessImpact, and exposing /readyz (gates traffic) and /healthz
// (informational, always 200) endpoints.
//
// The problem: in Kubernetes a pod that can't reach its database should be
// pulled out of the load-balancer rotation instead of serving errors. r8e
// derives that signal for free from the circuit-breaker state — when a
// critical dependency's breaker trips, /readyz flips to 503 and the pod stops
// receiving traffic until it recovers, all without a separate health-check
// system to maintain.
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

	// The registry is the aggregation point: every policy that registers here
	// contributes to the overall readiness verdict, and the HTTP handlers read
	// from it. Sharing one registry across policies is what lets a single
	// /readyz probe reflect the whole pod's health.
	reg := r8e.NewRegistry()

	// The database is the hard dependency. A circuit breaker gives us a health
	// signal for free: once it trips on repeated failures, the policy reports
	// itself unhealthy. FailureThreshold(2) keeps the demo short.
	dbPolicy := r8e.NewPolicy[string](
		"database",
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(2),
			r8e.RecoveryTimeout(30*time.Second),
		),
		// The pod cannot serve without the database, so gate readiness on it.
		// Without WithReadinessImpact a policy's health is reported but never
		// removes the pod from rotation — it stays informational only.
		r8e.WithReadinessImpact(),
		r8e.WithRegistry(reg),
	)

	// The api-gateway sits in front of the database. DependsOn wires the
	// health hierarchy so the gateway's status surfaces the database as a
	// nested dependency — operators see the root cause without correlating two
	// separate health checks. Note it does NOT carry WithReadinessImpact: its
	// readiness flows from the dependency it declares, not from itself.
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
	// Baseline: nothing has failed yet, so both breakers are closed, every
	// policy reports healthy, and readiness is true. This is the state we
	// expect to diverge from below.
	fmt.Println("=== Initial Health Status ===")
	printHealth(dbPolicy)
	printHealth(apiPolicy)
	printReadiness(reg)

	// --- Simulate database failures to open the circuit ---
	// Drive enough failures to cross FailureThreshold(2) and trip the breaker.
	// We loop 3 times (one more than the threshold) so the circuit is firmly
	// open by the time we re-check health.
	fmt.Println("\n=== Triggering database circuit breaker ===")

	for range 3 {
		//nolint:errcheck // example program — intentionally triggering failures
		_, _ = dbPolicy.Do(ctx, func(_ context.Context) (string, error) {
			return "", errors.New("connection refused")
		})
	}

	// Now the database is unhealthy. Because it carries WithReadinessImpact,
	// the registry's readiness flips to false; and because api-gateway
	// DependsOn it, the gateway's status shows the database failing underneath
	// it even though the gateway's own breaker is still closed.
	printHealth(dbPolicy)
	printHealth(apiPolicy)
	printReadiness(reg)

	// --- HTTP readiness endpoint ---
	// This is what Kubernetes actually probes. We use httptest instead of a
	// real listener so the example stays self-contained and deterministic —
	// the handler logic is identical to what a live server would run. With the
	// database breaker open, the readiness gate should respond 503 and pull
	// the pod from rotation.
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
	// /healthz answers a different question than /readyz: "is the process
	// alive?" rather than "should it receive traffic?". It returns 200 even
	// while the database is down — restarting the pod wouldn't fix a dependency
	// outage, so liveness must not trip on it, or Kubernetes would kill-loop a
	// perfectly running process.
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

// printHealth dumps one policy's health and recurses one level into its
// declared dependencies. Taking the HealthReporter interface (rather than a
// concrete *Policy) is what lets the same function print both policies — any
// type that can report health fits here.
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

// printReadiness asks the registry for the aggregate verdict — the single
// boolean a /readyz probe ultimately turns into a 200 or 503. It is false as
// soon as any policy carrying WithReadinessImpact is unhealthy.
func printReadiness(reg *r8e.Registry) {
	status := reg.CheckReadiness()
	fmt.Printf("  Readiness: ready=%v\n", status.Ready)
}
