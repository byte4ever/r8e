// Example 34-latency-percentiles: Demonstrates the always-on latency
// percentiles every policy records. Each Do() call's end-to-end duration feeds a
// sliding-window DDSketch, and Metrics() exposes the recent p50/p95/p99 — no
// option to enable, mirroring resilience4j's per-call timers.
//
// The problem it solves: an average hides a slow tail. A backend that answers in
// 10ms nine times out of ten and 150ms the tenth has a deceptively low mean, yet
// real users hit that slow path. Percentiles keep the tail visible — here most
// calls are fast but p99 sits far above p50 — which is what you actually alert on
// and what an adaptive timeout or hedge later tunes itself from.
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

	// A bare policy with no resilience options at all. The point is that the
	// latency percentiles below are always-on instrumentation: there is no
	// WithLatency(...) to enable, so even a policy that does nothing else still
	// records the distribution of its call durations.
	policy := r8e.NewPolicy[string]("api-call")

	// A backend that is usually quick (~10ms) but has a slow tail (~150ms) one
	// call in ten. This skew is deliberate: an average would average the rare
	// slow call away, whereas a p99 keeps it visible — which is exactly the gap
	// the percentiles are here to expose.
	backend := func(_ context.Context) (string, error) {
		if rand.IntN(10) == 0 {
			time.Sleep(150 * time.Millisecond)
		} else {
			time.Sleep(10 * time.Millisecond)
		}

		return "ok", nil
	}

	// Enough calls (200) for the rare ~10% slow path to land in the sliding
	// window often enough that p95/p99 settle on the tail rather than jittering.
	const calls = 200

	fmt.Printf("=== Driving %d calls through the policy ===\n", calls)

	// Every Do() feeds its end-to-end duration into the policy's sliding-window
	// sketch — the recording is a side effect of the call, no extra wiring.
	for range calls {
		if _, err := policy.Do(ctx, backend); err != nil {
			fmt.Printf("  unexpected error: %v\n", err)
		}
	}

	// --- Observability ---
	// Read the percentiles back from Metrics(). p50 reflects the typical fast
	// call; p99 reflects the slow tail. Seeing both side by side is the whole
	// lesson — a single average would collapse them into one misleading number.
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
