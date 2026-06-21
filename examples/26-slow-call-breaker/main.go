// Example 26-slow-call-breaker: Demonstrates the circuit breaker tripping on
// the SLOW-CALL rate, not just on failures. The simulated backend never
// returns an error — it just gets slow (a "brownout"). A failure-only breaker
// would never notice; with SlowCallRate enabled, once the fraction of slow
// calls in the window crosses the threshold the breaker opens and fast-fails
// with ErrCircuitOpen, shedding load off the struggling backend.
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
			// Keep the failure trip out of the way — this backend never errors.
			r8e.FailureThreshold(100),
			r8e.RecoveryTimeout(500*time.Millisecond),
			r8e.HalfOpenMaxAttempts(1),
			// A call slower than 50ms is "slow"; open once >=50% of the last 4
			// calls are slow.
			r8e.SlowCallRate(50*time.Millisecond, 0.5),
			r8e.SlowCallWindow(4),
			r8e.SlowCallMinCalls(4),
		),
		r8e.WithHooks(&r8e.Hooks{
			OnCircuitOpen:          func() { fmt.Println("  [hook] circuit breaker OPENED") },
			OnCircuitHalfOpen:      func() { fmt.Println("  [hook] circuit breaker HALF-OPEN") },
			OnCircuitClose:         func() { fmt.Println("  [hook] circuit breaker CLOSED") },
			OnSlowCallRateExceeded: func() { fmt.Println("  [hook] opened by SLOW-CALL rate") },
		}),
	)

	call := func(ctx context.Context) (string, error) {
		if slow {
			time.Sleep(100 * time.Millisecond) // brownout: slow but succeeds
		}

		return "ok", ctx.Err()
	}

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

	// Phase 1: backend is fast — the breaker stays closed.
	fmt.Println("=== Phase 1: backend fast, breaker closed ===")

	for i := 1; i <= 4; i++ {
		report(i)
	}

	// Phase 2: backend browns out — slow calls accumulate and open the breaker.
	fmt.Println("\n=== Phase 2: backend brownout (slow but successful) ===")

	slow = true

	for i := 5; i <= 9; i++ {
		report(i)
	}

	// Phase 3: backend recovers; after the recovery timeout a fast probe closes
	// the breaker again.
	fmt.Println("\n=== Phase 3: backend recovered, waiting for recovery timeout ===")
	time.Sleep(600 * time.Millisecond)

	slow = false

	report(10)
}
