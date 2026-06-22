// Example 37-chaos-injection: Demonstrates Polly-v8 / Simmy-style chaos
// injection. WithChaos inserts fault, latency, outcome, and behavior strategies
// at the innermost point of the chain — simulating a misbehaving downstream — so
// the policy's OWN resilience patterns get exercised: does the retry catch the
// injected fault? does the timeout catch the injected latency? Each strategy
// injects probabilistically and can be gated per call with ChaosEnabled for safe
// canary chaos in production. Strategies run in order, and a fault short-circuits
// the rest, so listing the fault first skips the latency wait when it fires.
//
//nolint:forbidigo // This is an example program; printing is fine here.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

// chaosKey gates chaos injection: only calls whose context carries it are
// subject to chaos, modelling a canary cohort (a header, a feature flag) rather
// than the whole fleet.
type ctxKey struct{}

func main() {
	ctx := context.Background()

	injected := make(map[string]int)

	// A policy that retries transient faults 4 times and bounds each call at
	// 100ms — then injects chaos at the very bottom to prove those patterns work.
	policy := r8e.NewPolicy[string](
		"chaos-demo",
		r8e.WithTimeout(100*time.Millisecond),
		r8e.WithRetry(4, r8e.ConstantBackoff(time.Millisecond)),
		r8e.WithFallback("served-from-fallback"),
		r8e.WithHooks(&r8e.Hooks{
			OnChaosInjected: func(kind string) { injected[kind]++ },
		}),
		r8e.WithChaos(
			// 30% of (canary) calls fail outright. Listed first so the latency
			// wait below is skipped when the fault fires (Polly's recommended
			// order). Retry re-rolls this on every attempt, so most calls still
			// succeed eventually.
			r8e.ChaosFault(0.3, errors.New("injected upstream failure"),
				r8e.ChaosEnabled(isCanary)),
			// 10% of (canary) calls hang 250ms — past the 100ms timeout, so the
			// timeout fires and (here) the fallback serves a default.
			r8e.ChaosLatency(0.1, 250*time.Millisecond,
				r8e.ChaosEnabled(isCanary)),
		),
	)

	healthy := func(_ context.Context) (string, error) { return "ok", nil }

	// Canary traffic: subject to chaos.
	canaryCtx := context.WithValue(ctx, ctxKey{}, true)

	const calls = 200

	var faults, fallbacks int

	for range calls {
		result, err := policy.Do(canaryCtx, healthy)
		if err != nil {
			faults++
		}

		if result == "served-from-fallback" {
			fallbacks++
		}
	}

	metrics := policy.Metrics()

	fmt.Printf("=== %d canary calls through retry+timeout+fallback, chaos at the core ===\n", calls)
	fmt.Printf("  chaos injected (fault):   %d\n", injected["fault"])
	fmt.Printf("  chaos injected (latency): %d\n", injected["latency"])
	fmt.Printf("  chaos injected (total):   %d\n", metrics.ChaosInjected)
	fmt.Printf("  retries (faults absorbed): %d\n", metrics.Retries)
	fmt.Printf("  timeouts (latency caught): %d\n", metrics.Timeouts)
	fmt.Printf("  fallbacks served:          %d\n", fallbacks)
	fmt.Printf("  calls that still errored:  %d\n", faults)

	// Non-canary traffic bypasses chaos entirely: the ChaosEnabled predicate
	// returns false, so nothing is injected even though the strategies exist.
	for range calls {
		if _, err := policy.Do(ctx, healthy); err != nil {
			fmt.Printf("  unexpected error: %v\n", err)
		}
	}

	fmt.Printf("\n=== %d production (non-canary) calls ===\n", calls)
	fmt.Printf("  chaos injected (total):   %d  (unchanged — gated off)\n", policy.Metrics().ChaosInjected)

	fmt.Println("\nThe retry soaked up most injected faults and the timeout caught the")
	fmt.Println("injected stragglers (fallback covering them) — proving the config reacts.")
	fmt.Println("Flip the ChaosEnabled predicate to roll chaos out beyond the canary.")
}

// isCanary reports whether the call belongs to the chaos canary cohort.
func isCanary(ctx context.Context) bool {
	canary, ok := ctx.Value(ctxKey{}).(bool)

	return ok && canary
}
