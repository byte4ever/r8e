// Example 36-adaptive-hedge: Demonstrates percentile-driven adaptive hedge delay.
// Instead of a fixed WithHedge(d), the policy fires the second concurrent attempt
// at a sliding-window percentile of recent PRIMARY latencies — clamp(p95 ×
// multiplier, floor, d) — so it only hedges genuine stragglers (the slowest ~5%)
// rather than every call past a guessed constant. The duration passed to WithHedge
// becomes the hard ceiling (the adaptive delay never exceeds it) and the warmup
// fallback. Pair it with a concurrency budget to cap the redundant load.
//
// The problem it solves: a fixed hedge delay is the same guess as a fixed timeout,
// with a worse failure mode. Set it too low and you double the load on every call,
// not just the slow ones; set it too high and the hedge fires too late to help.
// Anchoring it to the live p95 (Google's tail-at-scale rule) means the second
// attempt races only the genuinely slow tail, so you buy back latency without
// paying for it on the common fast path.
//
//nolint:forbidigo // This is an example program; printing is fine here.
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

	// A 500ms ceiling, but adapt down to ~p95 of the observed primary latency once
	// 20 successful calls have been seen. With a backend that is usually ~10ms but
	// occasionally a multi-hundred-ms straggler, the hedge fires just past the p95,
	// racing only the slow tail instead of doubling load on every call.
	policy := r8e.NewPolicy[string](
		"adaptive-hedge",
		r8e.WithHedge(
			// 500ms is the ceiling and warmup fallback — the adaptive delay only
			// ever pulls the hedge earlier than this, never later.
			500*time.Millisecond,
			r8e.AdaptiveHedge(
				// Fire at the p95: the fastest 95% of calls finish before the
				// hedge would ever start, so they pay no redundant cost at all.
				r8e.AdaptiveHedgePercentile(0.95),
				// ×1.0 means hedge exactly at the p95; raise it to wait deeper
				// into the tail, lower it to hedge more eagerly.
				r8e.AdaptiveHedgeMultiplier(1.0),
				// A floor so a burst of ultra-fast calls can't drive the delay to
				// near-zero and turn every call into a doubled request.
				r8e.AdaptiveHedgeFloor(5*time.Millisecond),
			),
		),
		// A hedge is a redundant request, so it can amplify load exactly when the
		// backend is already struggling. The budget bounds that blast radius: the
		// hedges may use at most 25% of in-flight executions, with a small floor
		// so low-traffic policies can still hedge at all.
		r8e.WithConcurrencyBudget(r8e.MaxRatio(0.25), r8e.MinConcurrency(5)),
	)

	backend := func(ctx context.Context) (string, error) {
		// ~10ms normally, but ~1 in 25 calls (4% — below the p95) is a 300ms
		// straggler, so the p95 stays in the fast band and the hedge fires early
		// enough to actually race the slow tail.
		latency := 10 * time.Millisecond
		if rand.IntN(25) == 0 { //nolint:gosec // non-crypto jitter is fine here
			latency = 300 * time.Millisecond
		}

		select {
		case <-time.After(latency):
			return "ok", nil
		case <-ctx.Done():
			return "", ctx.Err() //nolint:wrapcheck // surfacing cancellation
		}
	}

	// Enough calls (300) for the ~4% straggler rate to populate the window and
	// hold the p95 firmly in the fast band before we sample the adaptive delay.
	const calls = 300

	fmt.Printf("=== Driving %d calls (mostly ~10ms, some 300ms stragglers) ===\n", calls)

	for range calls {
		if _, err := policy.Do(ctx, backend); err != nil {
			fmt.Printf("  unexpected error: %v\n", err)
		}
	}

	// --- Observability ---
	// AdaptiveHedgeDelay is where the hedge currently fires; comparing it to the
	// p95 and the triggered/won counters shows the tail being raced, not every call.
	metrics := policy.Metrics()

	fmt.Println("\n=== After warmup ===")
	fmt.Printf("  observed p95:        %s\n", metrics.LatencyP95.Round(time.Millisecond))
	fmt.Printf("  adaptive hedge delay: %s  (was a 500ms ceiling)\n",
		metrics.AdaptiveHedgeDelay.Round(time.Millisecond))
	fmt.Printf("  hedges triggered:    %d\n", metrics.HedgesTriggered)
	fmt.Printf("  hedges won:          %d\n", metrics.HedgesWon)

	fmt.Println("\nThe hedge now fires just past the p95, so only genuine stragglers")
	fmt.Println("are raced — the redundant load stays small. Lower the percentile to")
	fmt.Println("hedge more eagerly; raise it to hedge only the very slowest calls.")
}
