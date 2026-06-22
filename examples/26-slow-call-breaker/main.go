// Example 26-slow-call-breaker: Demonstrates the circuit breaker tripping on
// the SLOW-CALL rate, not just on failures.
//
// The problem it solves: a backend in a "brownout" answers every request but
// answers slowly — no errors, just creeping latency. A failure-only breaker
// would never notice and would keep piling calls onto a struggling dependency,
// tying up the caller's goroutines and timeouts. With SlowCallRate enabled, once
// the fraction of slow calls in the recent window crosses the threshold the
// breaker opens and fast-fails with ErrCircuitOpen, shedding load until the
// backend recovers. The slow-call trip is additive to the failure trip — the
// breaker opens on whichever fires first.
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

	// slow flips the simulated backend between fast and brownout (slow but
	// still successful) responses.
	slow := false

	policy := r8e.NewPolicy[string]("slow-call-demo",
		r8e.WithCircuitBreaker(
			// Set the failure threshold deliberately high so it can never trip:
			// this backend never errors, so any trip we see must come from the
			// slow-call rate — that is the whole point of the demo.
			r8e.FailureThreshold(100),
			r8e.RecoveryTimeout(500*time.Millisecond),
			r8e.HalfOpenMaxAttempts(1),
			// A call slower than 50ms is "slow"; open once >=50% of the last 4
			// calls are slow. The window and min-calls are kept tiny so a single
			// brownout phase is enough to cross the threshold on a live run.
			r8e.SlowCallRate(50*time.Millisecond, 0.5),
			r8e.SlowCallWindow(4),
			r8e.SlowCallMinCalls(4),
		),
		// The hooks narrate every state transition so the run is self-explaining;
		// OnSlowCallRateExceeded specifically attributes the open to slow calls
		// rather than to failures.
		r8e.WithHooks(&r8e.Hooks{
			OnCircuitOpen:          func() { fmt.Println("  [hook] circuit breaker OPENED") },
			OnCircuitHalfOpen:      func() { fmt.Println("  [hook] circuit breaker HALF-OPEN") },
			OnCircuitClose:         func() { fmt.Println("  [hook] circuit breaker CLOSED") },
			OnSlowCallRateExceeded: func() { fmt.Println("  [hook] opened by SLOW-CALL rate") },
		}),
	)

	// call models the backend: in brownout it sleeps past the 50ms slow threshold
	// but still returns success, so only the slow-call detector can catch it.
	call := func(ctx context.Context) (string, error) {
		if slow {
			time.Sleep(100 * time.Millisecond) // brownout: slow but succeeds
		}

		return "ok", ctx.Err()
	}

	// report runs one call and classifies the outcome — a rejected call (open
	// breaker) is fast-failed without ever invoking the backend.
	report := func(i int) {
		_, err := policy.Do(ctx, call)
		switch {
		case errors.Is(err, r8e.ErrCircuitOpen):
			fmt.Printf("  call #%d rejected: circuit is open\n", i)
		case err != nil:
			fmt.Printf("  call #%d error: %v\n", i, err)
		default:
			fmt.Printf("  call #%d ok (slow-call rate %.0f%%)\n",
				i, policy.Metrics().SlowCallRate*100)
		}
	}

	// Phase 1: backend is fast. Every call beats the 50ms threshold, so the slow
	// fraction stays at zero and the breaker stays closed — the baseline.
	fmt.Println("=== Phase 1: backend fast, breaker closed ===")

	for i := 1; i <= 4; i++ {
		report(i)
	}

	// Phase 2: backend browns out. Successive slow-but-successful calls push the
	// slow fraction over 50%, the breaker opens, and later calls fast-fail with
	// ErrCircuitOpen instead of hanging on the slow backend.
	fmt.Println("\n=== Phase 2: backend brownout (slow but successful) ===")

	slow = true

	for i := 5; i <= 9; i++ {
		report(i)
	}

	// Phase 3: backend recovers. We sleep past the 500ms recovery timeout so the
	// breaker moves to half-open; the next fast call is the probe that, succeeding
	// quickly, closes the breaker and restores normal traffic.
	fmt.Println("\n=== Phase 3: backend recovered, waiting for recovery timeout ===")
	time.Sleep(600 * time.Millisecond)

	slow = false

	report(10)
}
