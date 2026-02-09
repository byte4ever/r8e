// Example 12-hooks: Demonstrates observability with lifecycle hooks.
// Shows all hook types firing during a policy execution.
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

	hooks := r8e.Hooks{
		OnRetry: func(attempt int, err error) {
			fmt.Printf(
				"  [OnRetry]            attempt=%d err=%v\n",
				attempt,
				err,
			)
		},
		OnCircuitOpen:      func() { fmt.Println("  [OnCircuitOpen]      circuit breaker opened") },
		OnCircuitClose:     func() { fmt.Println("  [OnCircuitClose]     circuit breaker closed") },
		OnCircuitHalfOpen:  func() { fmt.Println("  [OnCircuitHalfOpen]  circuit breaker half-open") },
		OnTimeout:          func() { fmt.Println("  [OnTimeout]          request timed out") },
		OnRateLimited:      func() { fmt.Println("  [OnRateLimited]      request rate limited") },
		OnBulkheadFull:     func() { fmt.Println("  [OnBulkheadFull]     bulkhead at capacity") },
		OnBulkheadAcquired: func() { fmt.Println("  [OnBulkheadAcquired] bulkhead slot acquired") },
		OnBulkheadReleased: func() { fmt.Println("  [OnBulkheadReleased] bulkhead slot released") },
		OnHedgeTriggered:   func() { fmt.Println("  [OnHedgeTriggered]   hedge request fired") },
		OnHedgeWon:         func() { fmt.Println("  [OnHedgeWon]         hedge request won") },
		OnFallbackUsed:     func(err error) { fmt.Printf("  [OnFallbackUsed]     error=%v\n", err) },
	}

	// --- Retry hooks ---
	fmt.Println("=== Retry Hooks ===")

	p := r8e.NewPolicy[string]("retry-hooks",
		r8e.WithRetry(3, r8e.ConstantBackoff(50*time.Millisecond)),
		r8e.WithFallback("fallback"),
		r8e.WithHooks(&hooks),
	)
	attempt := 0
	result, _ := p.Do( //nolint:errcheck // example program
		ctx,
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				return "", fmt.Errorf("fail %d", attempt)
			}

			return "success", nil
		},
	)
	fmt.Printf("  result: %q\n\n", result)

	// --- Bulkhead hooks ---
	fmt.Println("=== Bulkhead Hooks ===")

	bhPolicy := r8e.NewPolicy[string]("bh-hooks",
		r8e.WithBulkhead(1),
		r8e.WithHooks(&hooks),
	)
	result, _ = bhPolicy.Do( //nolint:errcheck // example program
		ctx,
		func(_ context.Context) (string, error) {
			return "bulkhead call", nil
		},
	)
	fmt.Printf("  result: %q\n\n", result)

	// --- Fallback hooks ---
	fmt.Println("=== Fallback Hooks ===")

	fbPolicy := r8e.NewPolicy[string]("fb-hooks",
		r8e.WithRetry(2, r8e.ConstantBackoff(50*time.Millisecond)),
		r8e.WithFallback("emergency"),
		r8e.WithHooks(&hooks),
	)
	result, _ = fbPolicy.Do( //nolint:errcheck // example program
		ctx,
		func(_ context.Context) (string, error) {
			return "", errors.New("total failure")
		},
	)
	fmt.Printf("  result: %q\n", result)
}
