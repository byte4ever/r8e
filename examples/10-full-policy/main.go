// Example 10-full-policy: Demonstrates composing all resilience patterns
// into a single policy. Patterns are auto-sorted by priority.
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

	// Compose all patterns. Order of options doesn't matter —
	// r8e automatically sorts them by execution priority.
	policy := r8e.NewPolicy[string]("full-policy",
		r8e.WithFallback("fallback value"),
		r8e.WithTimeout(2*time.Second),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(3),
			r8e.RecoveryTimeout(10*time.Second),
		),
		r8e.WithRateLimit(100),
		r8e.WithBulkhead(10),
		r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
		r8e.WithHedge(50*time.Millisecond),
		r8e.WithHooks(&r8e.Hooks{
			OnRetry:        func(attempt int, err error) { fmt.Printf("  [hook] retry #%d: %v\n", attempt, err) },
			OnTimeout:      func() { fmt.Println("  [hook] timeout") },
			OnFallbackUsed: func(err error) { fmt.Printf("  [hook] fallback used: %v\n", err) },
		}),
	)

	// The execution order is:
	//   Fallback → Timeout → CircuitBreaker → RateLimiter
	//     → Bulkhead → Retry → Hedge → fn()

	// --- Successful call ---
	fmt.Println("=== Successful Call (all patterns pass through) ===")

	result, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		return "all patterns composed successfully", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Failing call (triggers retry, then fallback) ---
	fmt.Println("=== Failing Call (retries exhausted → fallback) ===")

	attempt := 0
	result, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		return "", fmt.Errorf("failure #%d", attempt)
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Another failure — fallback kicks in ---
	fmt.Println("=== Another Failure (fallback) ===")
	// Create a fresh policy with only retry + fallback.
	freshPolicy := r8e.NewPolicy[string]("fresh-policy",
		r8e.WithRetry(2, r8e.ConstantBackoff(50*time.Millisecond)),
		r8e.WithFallback("emergency fallback"),
	)
	result, err = freshPolicy.Do(ctx, func(_ context.Context) (string, error) {
		return "", errors.New("total failure")
	})
	fmt.Printf("  result: %q, err: %v\n", result, err)
}
