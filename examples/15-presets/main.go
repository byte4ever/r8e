// Example 15-presets: Demonstrates StandardHTTPClient, AggressiveHTTPClient,
// and CachedClient presets.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// --- StandardHTTPClient preset ---
	// 5s timeout, 3 retries (100ms exponential), CB (5 failures, 30s recovery)
	fmt.Println("=== StandardHTTPClient Preset ===")
	stdPolicy := r8e.NewPolicy[string]("standard", r8e.StandardHTTPClient()...)

	attempt := 0
	result, err := stdPolicy.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		if attempt < 3 {
			return "", r8e.Transient(fmt.Errorf("attempt %d failed", attempt))
		}
		return "standard success", nil
	})
	fmt.Printf("  result: %q, err: %v (took %d attempts)\n\n", result, err, attempt)

	// --- AggressiveHTTPClient preset ---
	// 2s timeout, 5 retries (50ms exponential, 5s cap), CB (3 failures, 15s),
	// bulkhead(20)
	fmt.Println("=== AggressiveHTTPClient Preset ===")
	aggressivePolicy := r8e.NewPolicy[string]("aggressive", r8e.AggressiveHTTPClient()...)

	attempt = 0
	result, err = aggressivePolicy.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		if attempt < 4 {
			return "", r8e.Transient(fmt.Errorf("attempt %d failed", attempt))
		}
		return "aggressive success", nil
	})
	fmt.Printf("  result: %q, err: %v (took %d attempts)\n\n", result, err, attempt)

	// --- CachedClient preset ---
	// StandardHTTPClient + 5 minute stale cache
	fmt.Println("=== CachedClient Preset ===")
	cachedPolicy := r8e.NewPolicy[string]("cached", r8e.CachedClient()...)

	// First call populates the cache.
	result, err = cachedPolicy.Do(ctx, func(ctx context.Context) (string, error) {
		return fmt.Sprintf("fresh data at %s", time.Now().Format("15:04:05")), nil
	})
	fmt.Printf("  call 1: %q, err: %v\n", result, err)

	// Second call fails — stale cache serves the previous value.
	result, err = cachedPolicy.Do(ctx, func(ctx context.Context) (string, error) {
		return "", fmt.Errorf("service unavailable")
	})
	fmt.Printf("  call 2 (failure, served from cache): %q, err: %v\n", result, err)
}
