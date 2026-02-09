// Example 07-hedge: Demonstrates hedged requests reducing tail latency.
// After a delay, a second concurrent call is fired. The first to complete wins.
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

	policy := r8e.NewPolicy[string]("hedge-demo",
		r8e.WithHedge(100*time.Millisecond),
		r8e.WithHooks(&r8e.Hooks{
			OnHedgeTriggered: func() { fmt.Println("  [hook] hedge request triggered") },
			OnHedgeWon:       func() { fmt.Println("  [hook] hedge request WON") },
		}),
	)

	// Simulate a service with variable latency.
	callNum := 0
	call := func(ctx context.Context) (string, error) {
		callNum++
		// Random latency between 50ms and 300ms.
		latency := time.Duration(50+rand.IntN(250)) * time.Millisecond
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
