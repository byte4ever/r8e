// Example 42-nested-retry-budget: Demonstrates a nested (tree) retry budget.
//
// The problem it solves: a flat per-service retry budget (example 19) stops one
// service from storming its own downstream, but it does nothing about
// amplification ACROSS a call graph — if a gateway fans out to many services and
// each has its own healthy budget, a correlated outage can still let every
// service retry at once and bury a shared dependency. Parent nests each service
// budget under a gateway-wide budget: a service's retries are throttled by its
// own bucket AND roll up into the shared parent, so once the AGGREGATE retry
// pressure drains the parent, every service in the subtree is throttled — even
// ones whose own budget is still full. Amplification cannot cascade up the tree.
//
// This demo wires two service policies under one gateway budget, storms the
// first, and shows the second — locally healthy — get throttled by the drained
// shared parent.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// The gateway-wide budget is the ROOT of the tree: it caps the aggregate
	// retry pressure of everything beneath it.
	gateway := r8e.NewRetryBudget(r8e.MaxTokens(20), r8e.TokenRatio(0.1))

	// Each service gets its own budget nested under the gateway (Parent). A
	// service's retries drain its own bucket and, transitively, the gateway's.
	budgetA := r8e.NewRetryBudget(r8e.MaxTokens(10), r8e.TokenRatio(0.1), r8e.Parent(gateway))
	budgetB := r8e.NewRetryBudget(r8e.MaxTokens(10), r8e.TokenRatio(0.1), r8e.Parent(gateway))

	serviceA := r8e.NewPolicy[string]("service-a",
		r8e.WithRetry(5, r8e.ConstantBackoff(time.Millisecond)),
		r8e.WithSharedRetryBudget(budgetA),
	)
	serviceB := r8e.NewPolicy[string]("service-b",
		r8e.WithRetry(5, r8e.ConstantBackoff(time.Millisecond)),
		r8e.WithSharedRetryBudget(budgetB),
	)

	down := func(_ context.Context) (string, error) {
		return "", r8e.Transient(errors.New("downstream unavailable"))
	}

	// Phase 1: service A's downstream is down. A flood of failing calls storms
	// A's retries, draining A's own budget AND the shared gateway budget.
	fmt.Println("=== Phase 1: service A storms (its downstream is down) ===")

	var aFailures int

	for range 12 {
		if _, err := serviceA.Do(ctx, down); err != nil {
			aFailures++
		}
	}

	fmt.Printf("  service A: %d/12 calls failed; after the storm: gateway=%.1f/20, budgetA=%.1f/10, budgetB=%.1f/10\n",
		aFailures, gateway.Tokens(), budgetA.Tokens(), budgetB.Tokens())

	// Phase 2: service B is locally healthy (its bucket is untouched), but a
	// single failing call is NOT retried — the shared gateway budget, drained by
	// A, throttles B too. Amplification did not cascade: B's retries are capped
	// by the aggregate even though B itself never misbehaved.
	fmt.Println("\n=== Phase 2: service B is locally healthy but throttled by the shared gateway ===")

	var attempts int

	_, errB := serviceB.Do(ctx, func(_ context.Context) (string, error) {
		attempts++

		return "", r8e.Transient(errors.New("transient blip"))
	})

	fmt.Printf("  service B attempts: %d (err=%v; the retry was suppressed by the shared gateway budget)\n",
		attempts, errB)
	fmt.Printf("  budgetB.Exhausted()=%v — B's OWN bucket is still healthy\n", budgetB.Exhausted())
	fmt.Printf("  gateway.Exhausted()=%v — the shared parent is the bottleneck\n", gateway.Exhausted())

	fmt.Println("\nA storm in one leaf drains the shared parent and throttles its siblings, " +
		"so retry amplification cannot cascade up the call graph.")
}
