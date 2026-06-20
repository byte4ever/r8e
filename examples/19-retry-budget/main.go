// Example 19-retry-budget: Demonstrates the adaptive retry budget that throttles
// retries during a downstream outage to prevent a retry storm, and surfaces the
// throttling through a hook, metrics, and health.
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

	// A small budget (capacity 4) makes the throttling easy to see: retries are
	// suppressed once the bucket drains to half capacity. A success returns 0.1
	// tokens; each retryable failure removes one.
	var throttled int

	policy := r8e.NewPolicy[string]("flaky-downstream",
		r8e.WithHooks(&r8e.Hooks{
			OnRetryBudgetExceeded: func() { throttled++ },
		}),
		r8e.WithRetry(5, r8e.ConstantBackoff(10*time.Millisecond)),
		r8e.WithRetryBudget(r8e.MaxTokens(4), r8e.TokenRatio(0.1)),
	)

	down := errors.New("downstream unavailable")

	// --- A healthy bucket spends its budget on retries ---
	fmt.Println("=== Outage begins: budget absorbs the first retries ===")

	call := func(label string) {
		attempts := 0

		_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
			attempts++

			return "", r8e.Transient(down)
		})
		fmt.Printf("  %s: %d attempt(s), err: %v\n", label, attempts, err)
	}

	call("call 1")

	// --- The budget is now exhausted: retries are shed ---
	fmt.Println("\n=== Budget exhausted: retries are throttled ===")
	call("call 2")
	call("call 3")

	fmt.Printf("\n  OnRetryBudgetExceeded fired %d time(s)\n", throttled)

	// --- Metrics and health surface the throttling ---
	fmt.Println("\n=== Observability ===")

	metrics := policy.Metrics()
	fmt.Printf("  retries suppressed by budget: %d\n", metrics.RetryBudgetExceeded)
	fmt.Printf("  budget tokens remaining:      %.1f\n", metrics.RetryBudgetTokens)

	status := policy.HealthStatus()
	fmt.Printf("  health state:   %s\n", status.State)
	fmt.Printf("  criticality:    %s (readiness unaffected)\n", status.Criticality)
	fmt.Printf("  still healthy:  %t\n", status.Healthy)

	// --- Recovery: successes refill the bucket and retries resume ---
	fmt.Println("\n=== Recovery: a success refills the budget ===")

	for range 30 {
		if _, err := policy.Do(ctx, func(_ context.Context) (string, error) {
			return "ok", nil
		}); err != nil {
			fmt.Printf("  unexpected error during recovery: %v\n", err)
		}
	}

	fmt.Printf("  budget tokens after recovery: %.1f\n", policy.Metrics().RetryBudgetTokens)
	fmt.Printf("  exhausted: %t\n", policy.HealthStatus().State == r8e.ConditionRetryBudgetExhausted)
}
