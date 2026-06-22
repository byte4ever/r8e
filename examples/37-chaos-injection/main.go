// Example 37-chaos-injection: Demonstrates Polly-v8 / Simmy-style chaos
// injection. WithChaos inserts fault, latency, outcome, and behavior strategies
// at the innermost point of the chain — simulating a misbehaving downstream — so
// the policy's OWN resilience patterns get exercised: does the retry catch the
// injected fault? does the timeout catch the injected latency?
//
// The problem it solves: you can only trust your retry/timeout/fallback config
// once you have seen it react to real failures, but waiting for production to
// break is a poor test. Chaos manufactures those failures on demand. Each
// strategy injects probabilistically and can be gated per call with ChaosEnabled,
// so you can switch chaos on for a canary cohort in production without a redeploy
// and without touching the rest of the fleet. The example runs 200 canary calls
// through retry+timeout+fallback, then 200 gated-off production calls to prove
// the gate holds.
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

// ctxKey gates chaos injection: only calls whose context carries it are subject
// to chaos, modelling a canary cohort (a header, a feature flag) rather than the
// whole fleet. Using a private struct type as the key avoids collisions with any
// other value stashed in the context.
type ctxKey struct{}

func main() {
	ctx := context.Background()

	// Tally injections per strategy kind from the hook, so we can cross-check the
	// per-kind counts against the policy's own aggregate ChaosInjected counter.
	injected := make(map[string]int)

	// A policy that retries transient faults 4 times and bounds each call at
	// 100ms — then injects chaos at the very bottom to prove those patterns work.
	// The order of options is the order of the chain (outermost first): timeout
	// wraps retry wraps fallback wraps chaos, so the patterns get to react to the
	// faults that chaos manufactures from underneath them.
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

	// The downstream itself is perfectly healthy — every error and stall in this
	// run is manufactured by chaos, which keeps the demo's cause and effect clean.
	healthy := func(_ context.Context) (string, error) { return "ok", nil }

	// Canary traffic: the context carries the gate flag, so the ChaosEnabled
	// predicate lets chaos fire on these calls.
	canaryCtx := context.WithValue(ctx, ctxKey{}, true)

	const calls = 200

	var faults, fallbacks int

	for range calls {
		result, err := policy.Do(canaryCtx, healthy)
		// An error here would mean the config failed to absorb the injected chaos
		// (retry exhausted AND fallback unavailable) — we expect this to stay zero.
		if err != nil {
			faults++
		}

		// The fallback value is the tell that the timeout caught injected latency
		// the retry could not re-roll away within the attempt budget.
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
