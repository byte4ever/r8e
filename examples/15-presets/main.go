// Example 15-presets: Demonstrates StandardHTTPClient, AggressiveHTTPClient,
// and CachedClient presets.
//
// The problem: wiring a sensible timeout + retry + circuit-breaker stack by
// hand is repetitive and easy to get subtly wrong for every new client.
// Presets are curated option bundles for common shapes (a general-purpose HTTP
// client, a latency-sensitive aggressive one) so you start from a vetted
// baseline. They return a plain []any slice, so you can spread them into
// NewPolicy and still append your own options to override or extend.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"fmt"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// --- StandardHTTPClient preset ---
	// The conservative default: generous 5s timeout, a few retries, and a
	// breaker that only trips after 5 failures. Good when you'd rather wait
	// out a slow dependency than give up early.
	// 5s timeout, 3 retries (100ms exponential), CB (5 failures, 30s recovery)
	fmt.Println("=== StandardHTTPClient Preset ===")

	// Spread the preset's []any straight into NewPolicy — no extra options, so
	// the policy is exactly the curated baseline.
	stdPolicy := r8e.NewPolicy[string]("standard", r8e.StandardHTTPClient()...)

	// Fail twice with Transient errors (retry-worthy), succeed on the third.
	// The preset's 3 retry attempts are just enough to absorb this.
	attempt := 0
	result, err := stdPolicy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		if attempt < 3 {
			return "", r8e.Transient(fmt.Errorf("attempt %d failed", attempt))
		}

		return "standard success", nil
	})
	fmt.Printf(
		"  result: %q, err: %v (took %d attempts)\n\n",
		result,
		err,
		attempt,
	)

	// --- AggressiveHTTPClient preset ---
	// Tuned for latency-sensitive callers: a tight 2s timeout, more retries
	// with a shorter base delay so transient blips recover fast, a breaker
	// that trips sooner (3 failures) to fail fast, plus a bulkhead capping
	// concurrency so a stampede can't exhaust resources.
	// 2s timeout, 5 retries (50ms exponential, 5s cap), CB (3 failures, 15s),
	// bulkhead(20)
	fmt.Println("=== AggressiveHTTPClient Preset ===")

	aggressivePolicy := r8e.NewPolicy[string](
		"aggressive",
		r8e.AggressiveHTTPClient()...)

	// Fail the first three attempts; this preset allows 5 retries, so the
	// fourth attempt still lands within budget and succeeds.
	attempt = 0
	result, err = aggressivePolicy.Do(
		ctx,
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 4 {
				return "", r8e.Transient(
					fmt.Errorf("attempt %d failed", attempt),
				)
			}

			return "aggressive success", nil
		},
	)
	fmt.Printf(
		"  result: %q, err: %v (took %d attempts)\n\n",
		result,
		err,
		attempt,
	)

	// CachedClient has been removed. The stale cache is now a
	// standalone wrapper (r8e.StaleCache) backed by pluggable cache adapters.
	// See examples/08-stale-cache for the new approach.
}
