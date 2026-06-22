// Example 04-timeout: Demonstrates the global timeout pattern.
//
// A slow dependency that never returns can pin a goroutine (and the resources
// it holds) forever; WithTimeout bounds every call by deriving a deadline
// context and cancelling fn once it expires. The example also shows that an
// r8e-imposed timeout surfaces as ErrTimeout, while an externally cancelled
// parent context propagates its own error untouched — so callers can tell a
// deadline breach apart from a deliberate cancellation.
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

	// A 200ms deadline applies to the whole call. Pick it from the latency you
	// are willing to wait for: long enough that a healthy dependency finishes,
	// short enough that a stuck one is abandoned before it ties up resources.
	policy := r8e.NewPolicy[string]("timeout-demo",
		r8e.WithTimeout(200*time.Millisecond),
	)

	// --- Fast call: completes within the timeout ---
	// The happy path: fn returns well under the deadline, so the timeout is
	// invisible and the real result flows straight back.
	fmt.Println("=== Fast call (completes within timeout) ===")

	result, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		return "fast response", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Slow call: exceeds the timeout ---
	// fn would take a full second, but the policy cancels its context at 200ms.
	// Here fn selects on ctx.Done(): the timeout only frees the caller — fn must
	// honour cancellation to actually stop working, otherwise it keeps running
	// in the background even though nobody is waiting for it.
	fmt.Println("=== Slow call (exceeds 200ms timeout) ===")

	_, err = policy.Do(ctx, func(ctx context.Context) (string, error) {
		select {
		case <-time.After(1 * time.Second):
			return "this won't be reached", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	// A deadline breach surfaces as ErrTimeout (not a raw context error), so the
	// caller can react specifically to "we gave up waiting".
	if errors.Is(err, r8e.ErrTimeout) {
		fmt.Printf("  err: %v (timed out as expected)\n\n", err)
	}

	// --- Timeout distinguishes from parent context cancellation ---
	// Now the caller's own context dies first (at 50ms, before the 200ms
	// deadline). The policy must not relabel that as ErrTimeout: an external
	// cancellation — a shutdown, a request abort — is a different event and the
	// caller deserves to see its own error, not a fabricated timeout.
	fmt.Println("=== Parent context cancelled ===")

	parentCtx, cancel := context.WithCancel(ctx)

	// Cancel the parent at 50ms, ahead of both the deadline and fn's 1s sleep.
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err = policy.Do(parentCtx, func(ctx context.Context) (string, error) {
		select {
		case <-time.After(1 * time.Second):
			return "this won't be reached", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	})
	// context.Canceled comes back, proving the parent's error wins over the
	// policy's own ErrTimeout.
	fmt.Printf("  err: %v (parent cancelled, not timeout)\n", err)
}
