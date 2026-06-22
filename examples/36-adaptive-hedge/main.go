// Example 36-adaptive-hedge: Demonstrates percentile-driven adaptive hedge delay.
// Instead of a fixed WithHedge(d), the policy fires the second concurrent attempt
// at a sliding-window percentile of recent PRIMARY latencies — clamp(p95 ×
// multiplier, floor, d) — so it only hedges genuine stragglers (the slowest ~5%)
// rather than every call past a guessed constant. The duration passed to WithHedge
// becomes the hard ceiling (the adaptive delay never exceeds it) and the warmup
// fallback. Pair it with a concurrency budget to cap the redundant load.
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
			500*time.Millisecond,
			r8e.AdaptiveHedge(
				r8e.AdaptiveHedgePercentile(0.95),
				r8e.AdaptiveHedgeMultiplier(1.0),
				r8e.AdaptiveHedgeFloor(5*time.Millisecond),
			),
		),
		// Cap the extra load the hedges add: at most 25% of in-flight executions,
		// with a small floor.
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

	const calls = 300

	fmt.Printf("=== Driving %d calls (mostly ~10ms, some 300ms stragglers) ===\n", calls)

	for range calls {
		if _, err := policy.Do(ctx, backend); err != nil {
			fmt.Printf("  unexpected error: %v\n", err)
		}
	}

	// --- Observability ---
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
