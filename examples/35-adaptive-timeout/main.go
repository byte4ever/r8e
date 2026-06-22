// Example 35-adaptive-timeout: Demonstrates percentile-driven adaptive timeout.
// Instead of a fixed WithTimeout(d), the policy sizes each call's deadline from a
// sliding window of recent SUCCESSFUL latencies — clamp(p99 × multiplier, floor,
// d) — so the timeout tracks the backend's real service time. The duration passed
// to WithTimeout becomes the hard ceiling (the adaptive value never exceeds it)
// and the warmup fallback used until enough samples accumulate.
//
// The problem it solves: a fixed timeout is a guess. Set it too tight and you cut
// healthy-but-variable calls; set it too loose (the usual reflex) and a genuine
// straggler ties up a slot for the full duration before failing. Pinning the
// deadline to the observed p99 keeps it both safe for normal calls and quick to
// give up on a real outlier — and it follows the backend if its latency drifts.
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
			// The 1s here is the safety ceiling and the warmup fallback, not the
			// operating value — the adaptive logic only ever tightens below it,
			// so a cold or low-traffic policy still gets the operator's full budget.
			time.Second,
			r8e.AdaptiveTimeout(
				// Size the deadline off the p99 (only the slowest ~1% of healthy
				// calls is allowed to breach the multiplier-scaled budget).
				r8e.AdaptiveTimeoutPercentile(0.99),
				// ×2 leaves headroom above p99 so normal jitter never trips the
				// timeout; tighten it to cut stragglers sooner at that risk.
				r8e.AdaptiveTimeoutMultiplier(2.0),
				// A floor so a momentarily ultra-fast window can't collapse the
				// timeout to near-zero and start failing legitimate calls.
				r8e.AdaptiveTimeoutFloor(20*time.Millisecond),
			),
		),
	)

	// A steady ~10ms backend. With no variance the p99 sits right at 10ms, making
	// the gap between the adapted timeout (~20ms) and the 1s ceiling obvious.
	backend := func(_ context.Context) (string, error) {
		time.Sleep(10 * time.Millisecond)

		return "ok", nil
	}

	// Well past the 20-sample warmup so the window is full and the adaptive value
	// has fully replaced the 1s fallback before we read it back.
	const calls = 200

	fmt.Printf("=== Warming up with %d ~10ms calls ===\n", calls)

	for range calls {
		if _, err := policy.Do(ctx, backend); err != nil {
			fmt.Printf("  unexpected error: %v\n", err)
		}
	}

	// --- Observability ---
	// AdaptiveTimeout reports the deadline the policy would apply right now;
	// comparing it to the observed p99 shows the multiplier headroom at work.
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
