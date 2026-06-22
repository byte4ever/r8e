// Example 34-latency-percentiles: Demonstrates the always-on latency
// percentiles every policy records. Each Do() call's end-to-end duration feeds a
// sliding-window DDSketch, and Metrics() exposes the recent p50/p95/p99 — no
// option to enable, mirroring resilience4j's per-call timers. Percentiles make a
// slow tail visible that an average would hide: here most calls are fast but a
// few are slow, so p99 sits far above p50.
//
//nolint:forbidigo,gosec // This is an example program; math/rand is fine here.
package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// No latency option exists — the percentiles are recorded for free.
	policy := r8e.NewPolicy[string]("api-call")

	// A backend that is usually quick (~10ms) but has a slow tail (~150ms) one
	// call in ten — the shape percentiles are meant to reveal.
	backend := func(_ context.Context) (string, error) {
		if rand.IntN(10) == 0 {
			time.Sleep(150 * time.Millisecond)
		} else {
			time.Sleep(10 * time.Millisecond)
		}

		return "ok", nil
	}

	const calls = 200

	fmt.Printf("=== Driving %d calls through the policy ===\n", calls)

	for range calls {
		if _, err := policy.Do(ctx, backend); err != nil {
			fmt.Printf("  unexpected error: %v\n", err)
		}
	}

	// --- Observability ---
	fmt.Println("\n=== Recent latency percentiles ===")

	m := policy.Metrics()
	fmt.Printf("  samples in window: %d\n", m.LatencySamples)
	fmt.Printf("  p50: %s  (typical call)\n", m.LatencyP50.Round(time.Millisecond))
	fmt.Printf("  p95: %s\n", m.LatencyP95.Round(time.Millisecond))
	fmt.Printf("  p99: %s  (slow tail an average would hide)\n",
		m.LatencyP99.Round(time.Millisecond))

	fmt.Println("\nFeed these percentiles to dashboards, or let a future adaptive")
	fmt.Println("timeout/hedge tune itself from the observed p99.")
}
