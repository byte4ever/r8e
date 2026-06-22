// Example 19-retry-budget: Demonstrates the adaptive retry budget that throttles
// retries during a downstream outage to prevent a retry storm.
//
// The problem: when a dependency falls over, every caller's retries pile on top
// of the original load, kicking the struggling service while it is down. The
// budget is a gRPC-style token bucket — successes refill it, retryable failures
// drain it — and once it sits at or below half capacity, retries are suppressed
// so the dependency gets room to recover. This program walks through draining the
// bucket, watching retries get shed, and refilling it back with successes, with
// the whole cycle visible through a hook, the metrics snapshot, and health.
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
	// tokens; each retryable failure removes one — so it takes many successes to
	// undo a single failure, which is exactly what keeps a flapping dependency
	// from getting hammered. The hook just counts how often we shed a retry.
	var throttled int

	// WithRetry sets the ceiling (up to 5 attempts); WithRetryBudget is the
	// governor on top of it. The budget never blocks the *first* attempt of a
	// call — only the 2nd-and-later retries — so requests keep flowing even when
	// the bucket is empty; what disappears is the amplification.
	policy := r8e.NewPolicy[string]("flaky-downstream",
		r8e.WithHooks(&r8e.Hooks{
			OnRetryBudgetExceeded: func() { throttled++ },
		}),
		r8e.WithRetry(5, r8e.ConstantBackoff(10*time.Millisecond)),
		r8e.WithRetryBudget(r8e.MaxTokens(4), r8e.TokenRatio(0.1)),
	)

	down := errors.New("downstream unavailable")

	// The bucket starts full, so the first call gets to spend its budget on real
	// retries. We count attempts inside fn to see how many times the downstream
	// is actually hit — that number is what the budget will clamp down later.
	fmt.Println("=== Outage begins: budget absorbs the first retries ===")

	// Transient marks the error as retryable; without it the retry stage would
	// give up immediately and the budget would never come into play.
	call := func(label string) {
		attempts := 0

		_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
			attempts++

			return "", r8e.Transient(down)
		})
		fmt.Printf("  %s: %d attempt(s), err: %v\n", label, attempts, err)
	}

	call("call 1")

	// Call 1's failed retries already drained the bucket below half, so from here
	// on retries are shed: calls 2 and 3 should report just 1 attempt each — the
	// first try runs, but the budget refuses to amplify it into a storm.
	fmt.Println("\n=== Budget exhausted: retries are throttled ===")
	call("call 2")
	call("call 3")

	fmt.Printf("\n  OnRetryBudgetExceeded fired %d time(s)\n", throttled)

	// The throttling isn't just a side effect you have to infer — it's reported.
	// Metrics give you the running counters/gauges for dashboards, and health
	// gives you a coarse state for probes. Note the budget degrades health but
	// deliberately does NOT flip readiness: first attempts still flow, so the
	// service is degraded, not down, and we don't want orchestrators evicting it.
	fmt.Println("\n=== Observability ===")

	metrics := policy.Metrics()
	fmt.Printf("  retries suppressed by budget: %d\n", metrics.RetryBudgetExceeded)
	fmt.Printf("  budget tokens remaining:      %.1f\n", metrics.RetryBudgetTokens)

	status := policy.HealthStatus()
	fmt.Printf("  health state:   %s\n", status.State)
	fmt.Printf("  criticality:    %s (readiness unaffected)\n", status.Criticality)
	fmt.Printf("  still healthy:  %t\n", status.Healthy)

	// Recovery is asymmetric on purpose: each success only returns 0.1 tokens, so
	// we need a sustained run of healthy calls to climb back above the half-mark
	// where retries are re-enabled. 30 successes is comfortably enough to refill
	// the capacity-4 bucket and clear the exhausted condition.
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
