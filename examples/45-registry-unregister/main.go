// Example 45-registry-unregister: Demonstrates Registry.Unregister, which takes
// a named policy out of the registry it joined.
//
// The problem it solves: a named policy registers itself with DefaultRegistry
// (or the registry given with WithRegistry), and the registry keeps it until
// something removes it. A service that builds a named policy per job, per
// tenant or per test would otherwise grow its registry without bound, and its
// health endpoint would keep listing policies that no longer run.
//
// Unregister removes a policy by identity once it is no longer needed. Another
// policy with the same name stays registered. Policy.Registry returns the
// registry the policy joined, so the same line removes it whatever registry it
// was built with.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// Phase 1: each job builds its own named policy and removes it when the
	// job ends, so the registry only lists the jobs that are running.
	fmt.Println("=== Phase 1: one policy per job, removed when the job ends ===")

	for id := 1; id <= 3; id++ {
		runJob(ctx, id)
		fmt.Printf("  after job %d: %d registered\n", id, len(registeredNames()))
	}

	// Phase 2: two policies share a name. Unregister compares the policies
	// themselves, so removing one leaves the other in place.
	fmt.Println("\n=== Phase 2: removal is by identity, not by name ===")

	first := r8e.NewPolicy[string]("tenant-api")
	second := r8e.NewPolicy[string]("tenant-api")

	fmt.Printf("  registered: %v\n", registeredNames())
	fmt.Printf("  unregister the first: %v\n", r8e.DefaultRegistry().Unregister(first))
	fmt.Printf("  registered: %v\n", registeredNames())
	fmt.Printf("  unregister the first again: %v\n", r8e.DefaultRegistry().Unregister(first))
	fmt.Printf("  unregister the second: %v\n", r8e.DefaultRegistry().Unregister(second))
	fmt.Printf("  registered: %v\n", registeredNames())

	// Phase 3: a policy built with its own registry is not in DefaultRegistry.
	// Policy.Registry finds the right one; an unnamed policy joined none.
	fmt.Println("\n=== Phase 3: the policy knows the registry it joined ===")

	batch := r8e.NewPolicy[string]("tenant-batch", r8e.WithRegistry(r8e.NewRegistry()))
	unnamed := r8e.NewPolicy[string]("")

	fmt.Printf("  unregister from DefaultRegistry: %v\n", r8e.DefaultRegistry().Unregister(batch))
	fmt.Printf("  unregister from its own registry: %v\n", batch.Registry().Unregister(batch))
	fmt.Printf("  unnamed policy, no registry: %v\n", unnamed.Registry() == nil)
	fmt.Printf("  unregister the unnamed policy: %v\n", unnamed.Registry().Unregister(unnamed))
}

// runJob runs one job behind a policy named after it, and takes the policy out
// of the registry when the job returns.
func runJob(ctx context.Context, id int) {
	policy := r8e.NewPolicy[string](fmt.Sprintf("job-%d", id),
		r8e.WithTimeout(time.Second),
	)
	defer policy.Registry().Unregister(policy)

	result, err := policy.Do(ctx, func(context.Context) (string, error) {
		return fmt.Sprintf("job %d done", id), nil
	})
	if err != nil {
		fmt.Printf("  job %d failed: %v\n", id, err)

		return
	}

	fmt.Printf("  %s (registered while running: %v)\n", result, registeredNames())
}

// registeredNames lists the names of the policies DefaultRegistry reports on.
func registeredNames() []string {
	policies := r8e.DefaultRegistry().Health().Policies

	names := make([]string, 0, len(policies))
	for _, status := range policies {
		names = append(names, status.Name)
	}

	return names
}
