// Example 05-rate-limiter: Demonstrates the token-bucket rate limiter
// in both reject and blocking modes.
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

	// --- Reject mode: bursts above the rate are rejected ---
	fmt.Println("=== Reject Mode (5 tokens/sec) ===")

	rejectPolicy := r8e.NewPolicy[string]("rl-reject",
		r8e.WithRateLimit(5), // 5 tokens per second
	)

	// Fire 8 rapid calls — first ~5 should succeed, rest should be rejected.
	for i := 1; i <= 8; i++ {
		result, err := rejectPolicy.Do(
			ctx,
			func(_ context.Context) (string, error) {
				return fmt.Sprintf("request %d", i), nil
			},
		)
		switch {
		case errors.Is(err, r8e.ErrRateLimited):
			fmt.Printf("  request %d: RATE LIMITED\n", i)
		case err != nil:
			fmt.Printf("  request %d: error: %v\n", i, err)
		default:
			fmt.Printf("  request %d: %s\n", i, result)
		}
	}

	// --- Blocking mode: waits for a token ---
	fmt.Println("\n=== Blocking Mode (5 tokens/sec) ===")

	blockingPolicy := r8e.NewPolicy[string]("rl-blocking",
		r8e.WithRateLimit(5, r8e.RateLimitBlocking()),
	)

	// Wait a moment for fresh tokens.
	time.Sleep(1100 * time.Millisecond)

	fmt.Println(
		"Sending 7 requests (first ~5 instant, rest wait for tokens)...",
	)

	start := time.Now()

	for i := 1; i <= 7; i++ {
		result, err := blockingPolicy.Do(
			ctx,
			func(_ context.Context) (string, error) {
				return fmt.Sprintf("request %d", i), nil
			},
		)

		elapsed := time.Since(start).Truncate(time.Millisecond)
		if err != nil {
			fmt.Printf("  request %d at %v: error: %v\n", i, elapsed, err)
		} else {
			fmt.Printf("  request %d at %v: %s\n", i, elapsed, result)
		}
	}
}
