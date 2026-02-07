// Example 08-stale-cache: Demonstrates serving cached values on failure.
// A successful call populates the cache; subsequent failures serve stale data.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	policy := r8e.NewPolicy[string]("stale-cache-demo",
		r8e.WithStaleCache(2*time.Second),
		r8e.WithHooks(r8e.Hooks{
			OnCacheRefreshed: func() { fmt.Println("  [hook] cache refreshed") },
			OnStaleServed:    func(age time.Duration) { fmt.Printf("  [hook] serving stale data (age: %v)\n", age) },
		}),
	)

	shouldFail := false

	call := func(ctx context.Context) (string, error) {
		if shouldFail {
			return "", fmt.Errorf("downstream unavailable")
		}
		return fmt.Sprintf("fresh data at %v", time.Now().Format("15:04:05.000")), nil
	}

	// Call 1: Success — populates cache.
	fmt.Println("=== Call 1: Success (populates cache) ===")
	result, err := policy.Do(ctx, call)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Call 2: Downstream fails — stale cache serves the previous value.
	shouldFail = true
	fmt.Println("=== Call 2: Failure (served from stale cache) ===")
	result, err = policy.Do(ctx, call)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Call 3: Still failing but within TTL — stale cache still works.
	time.Sleep(500 * time.Millisecond)
	fmt.Println("=== Call 3: Failure again (still within TTL) ===")
	result, err = policy.Do(ctx, call)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Call 4: Wait for TTL to expire — cache is too old, error propagates.
	fmt.Println("=== Call 4: Waiting for TTL expiry (2s)... ===")
	time.Sleep(2 * time.Second)
	_, err = policy.Do(ctx, call)
	fmt.Printf("  err: %v (cache expired, error propagated)\n", err)
}
