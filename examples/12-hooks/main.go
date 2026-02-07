// Example 12-hooks: Demonstrates observability with lifecycle hooks.
// Shows all hook types firing during a policy execution.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	hooks := r8e.Hooks{
		OnRetry:            func(attempt int, err error) { fmt.Printf("  [OnRetry]            attempt=%d err=%v\n", attempt, err) },
		OnCircuitOpen:      func() { fmt.Println("  [OnCircuitOpen]      circuit breaker opened") },
		OnCircuitClose:     func() { fmt.Println("  [OnCircuitClose]     circuit breaker closed") },
		OnCircuitHalfOpen:  func() { fmt.Println("  [OnCircuitHalfOpen]  circuit breaker half-open") },
		OnTimeout:          func() { fmt.Println("  [OnTimeout]          request timed out") },
		OnRateLimited:      func() { fmt.Println("  [OnRateLimited]      request rate limited") },
		OnBulkheadFull:     func() { fmt.Println("  [OnBulkheadFull]     bulkhead at capacity") },
		OnBulkheadAcquired: func() { fmt.Println("  [OnBulkheadAcquired] bulkhead slot acquired") },
		OnBulkheadReleased: func() { fmt.Println("  [OnBulkheadReleased] bulkhead slot released") },
		OnStaleServed:      func(age time.Duration) { fmt.Printf("  [OnStaleServed]      serving stale data (age: %v)\n", age) },
		OnCacheRefreshed:   func() { fmt.Println("  [OnCacheRefreshed]   cache updated") },
		OnHedgeTriggered:   func() { fmt.Println("  [OnHedgeTriggered]   hedge request fired") },
		OnHedgeWon:         func() { fmt.Println("  [OnHedgeWon]         hedge request won") },
		OnFallbackUsed:     func(err error) { fmt.Printf("  [OnFallbackUsed]     error=%v\n", err) },
	}

	// --- Retry hooks ---
	fmt.Println("=== Retry Hooks ===")
	p := r8e.NewPolicy[string]("retry-hooks",
		r8e.WithRetry(3, r8e.ConstantBackoff(50*time.Millisecond)),
		r8e.WithFallback("fallback"),
		r8e.WithHooks(hooks),
	)
	attempt := 0
	result, _ := p.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		if attempt < 3 {
			return "", fmt.Errorf("fail %d", attempt)
		}
		return "success", nil
	})
	fmt.Printf("  result: %q\n\n", result)

	// --- Bulkhead hooks ---
	fmt.Println("=== Bulkhead Hooks ===")
	bhPolicy := r8e.NewPolicy[string]("bh-hooks",
		r8e.WithBulkhead(1),
		r8e.WithHooks(hooks),
	)
	result, _ = bhPolicy.Do(ctx, func(ctx context.Context) (string, error) {
		return "bulkhead call", nil
	})
	fmt.Printf("  result: %q\n\n", result)

	// --- Stale cache hooks ---
	fmt.Println("=== Stale Cache Hooks ===")
	cachePolicy := r8e.NewPolicy[string]("cache-hooks",
		r8e.WithStaleCache(5*time.Second),
		r8e.WithHooks(hooks),
	)
	// First call: populates cache.
	cachePolicy.Do(ctx, func(ctx context.Context) (string, error) {
		return "cached data", nil
	})
	// Second call: fails, serves stale.
	result, _ = cachePolicy.Do(ctx, func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("downstream error")
	})
	fmt.Printf("  result: %q\n", result)
}
