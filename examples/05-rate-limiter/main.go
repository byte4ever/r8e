// Example 05-rate-limiter: Demonstrates the token-bucket rate limiter.
//
// A rate limiter caps how often a dependency is called so a burst of traffic
// can't overwhelm it (or blow a third-party quota). This example runs the same
// 5 tokens/sec limit under both policies the limiter offers: reject mode sheds
// excess instantly with ErrRateLimited (fail fast, let the caller retry), while
// blocking mode parks the caller until a token refills (smooth throughput, no
// drops). Which one you want depends on whether dropping work or delaying it is
// cheaper for your call site.
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
	// Reject mode (the default) sheds load the instant the bucket is empty. Use
	// it at edges that can afford to say "no" — an API gateway, a public
	// endpoint — where a fast failure the caller can retry beats queueing work.
	fmt.Println("=== Reject Mode (5 tokens/sec) ===")

	rejectPolicy := r8e.NewPolicy[string]("rl-reject",
		r8e.WithRateLimit(5), // 5 tokens per second
	)

	// Fire 8 back-to-back calls into a bucket that holds ~5 tokens. The first
	// few drain the burst; the rest find it empty and bounce immediately, no
	// waiting — that's the whole point of shedding rather than queueing.
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
	// Same 5/sec budget, but RateLimitBlocking() makes the limiter wait for a
	// token instead of rejecting. This is for places that must not drop work —
	// background workers, batch pipelines — where you want to pace throughput
	// and trade latency for completeness.
	fmt.Println("\n=== Blocking Mode (5 tokens/sec) ===")

	blockingPolicy := r8e.NewPolicy[string]("rl-blocking",
		r8e.WithRateLimit(5, r8e.RateLimitBlocking()),
	)

	// The reject-mode burst above left the bucket drained. Sleep just over a
	// second so it refills to capacity, otherwise the first "instant" requests
	// below would already be waiting and the demo's timing would be muddied.
	time.Sleep(1100 * time.Millisecond)

	fmt.Println(
		"Sending 7 requests (first ~5 instant, rest wait for tokens)...",
	)

	// Stamp a baseline so each request can report how long it actually waited.
	start := time.Now()

	for i := 1; i <= 7; i++ {
		result, err := blockingPolicy.Do(
			ctx,
			func(_ context.Context) (string, error) {
				return fmt.Sprintf("request %d", i), nil
			},
		)

		// Elapsed time tells the story: the first ~5 fire near 0ms off the
		// initial burst, then each later one lands ~200ms apart as tokens
		// trickle back at 5/sec — the smoothing that blocking mode buys you.
		elapsed := time.Since(start).Truncate(time.Millisecond)
		if err != nil {
			fmt.Printf("  request %d at %v: error: %v\n", i, elapsed, err)
		} else {
			fmt.Printf("  request %d at %v: %s\n", i, elapsed, result)
		}
	}
}
