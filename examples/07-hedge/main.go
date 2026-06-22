// Example 07-hedge: Demonstrates hedged (speculative) requests that cut tail
// latency. When a service has a fat tail (p99 >> p50), most calls are fast but
// the occasional slow one dominates user experience. Hedging fixes this: if the
// primary call hasn't answered within the hedge delay, a second concurrent call
// is fired, and the first response to arrive wins (the loser's context is
// cancelled). You trade a little redundant load for a much tighter p99.
//
//nolint:forbidigo // This is an example program.
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

	// Hedge delay of 100ms: only calls slower than this pay the cost of a
	// second request, so fast calls (the common case) are untouched. The hooks
	// let us observe the otherwise-invisible hedge machinery — when a hedge
	// fires and when it beats the primary.
	policy := r8e.NewPolicy[string]("hedge-demo",
		r8e.WithHedge(100*time.Millisecond),
		r8e.WithHooks(&r8e.Hooks{
			OnHedgeTriggered: func() { fmt.Println("  [hook] hedge request triggered") },
			OnHedgeWon:       func() { fmt.Println("  [hook] hedge request WON") },
		}),
	)

	// Simulate a service with a wide latency spread. The 50–300ms range
	// straddles the 100ms hedge delay so some calls finish before the hedge
	// fires and others trigger it — exactly the mix that makes hedging worth it.
	callNum := 0
	call := func(ctx context.Context) (string, error) {
		callNum++
		// Random latency between 50ms and 300ms.
		latency := time.Duration(50+rand.IntN(250)) * time.Millisecond
		// Honour ctx.Done() so the losing call actually stops when the winner
		// returns — otherwise the hedge would leak a goroutine doing dead work.
		select {
		case <-time.After(latency):
			return fmt.Sprintf("response (latency: %v)", latency), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	fmt.Println("=== Hedged Requests (hedge delay: 100ms) ===")
	fmt.Println("Running 5 calls. If primary is slow (>100ms), a hedge fires.")
	fmt.Println()

	// Run several calls so the randomized latency exercises both paths:
	// fast primaries that win outright, and slow ones where the hedge kicks in.
	// Watch the elapsed time — even a slow primary stays near the hedge delay
	// plus the faster of the two latencies, never the full worst case.
	for i := 1; i <= 5; i++ {
		start := time.Now()
		result, err := policy.Do(ctx, call)

		elapsed := time.Since(start).Truncate(time.Millisecond)
		if err != nil {
			fmt.Printf("  call %d: error: %v (%v)\n", i, err, elapsed)
		} else {
			fmt.Printf("  call %d: %s (total: %v)\n", i, result, elapsed)
		}

		fmt.Println()
	}
}
