// Example 35-adaptive-timeout: Demonstrates percentile-driven adaptive timeout.
// Instead of a fixed WithTimeout(d), the policy sizes each call's deadline from a
// sliding window of recent SUCCESSFUL latencies — clamp(p99 × multiplier, floor,
// d) — so the timeout tracks the backend's real service time. The duration passed
// to WithTimeout becomes the hard ceiling (the adaptive value never exceeds it)
// and the warmup fallback used until enough samples accumulate.
//
//nolint:forbidigo // This is an example program; printing is fine here.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// A 1s hard ceiling, but adapt down to ~p99 × 2 of the observed latency once
	// 20 successful calls have been seen. With a ~10ms backend the adaptive timeout
	// settles far below the 1s ceiling, so a genuine straggler is cut quickly.
	policy := r8e.NewPolicy[string](
		"adaptive-timeout",
		r8e.WithTimeout(
			time.Second,
			r8e.AdaptiveTimeout(
				r8e.AdaptiveTimeoutPercentile(0.99),
				r8e.AdaptiveTimeoutMultiplier(2.0),
				r8e.AdaptiveTimeoutFloor(20*time.Millisecond),
			),
		),
	)

	backend := func(_ context.Context) (string, error) {
		time.Sleep(10 * time.Millisecond)

		return "ok", nil
	}

	const calls = 200

	fmt.Printf("=== Warming up with %d ~10ms calls ===\n", calls)

	for range calls {
		if _, err := policy.Do(ctx, backend); err != nil {
			fmt.Printf("  unexpected error: %v\n", err)
		}
	}

	// --- Observability ---
	m := policy.Metrics()

	fmt.Println("\n=== After warmup ===")
	fmt.Printf("  observed p99:       %s\n", m.LatencyP99.Round(time.Millisecond))
	fmt.Printf("  adaptive timeout:   %s  (was a 1s ceiling)\n",
		m.AdaptiveTimeout.Round(time.Millisecond))
	fmt.Printf("  timeouts so far:    %d\n", m.Timeouts)

	fmt.Println("\nThe deadline now tracks the backend, not a guessed constant —")
	fmt.Println("tighten the multiplier to cut stragglers sooner, raise it to tolerate")
	fmt.Println("a wider tail. It never exceeds the WithTimeout ceiling.")
}
