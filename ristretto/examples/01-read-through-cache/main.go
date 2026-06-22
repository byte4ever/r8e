// Example 01-read-through-cache: back r8e's read-through cache policy with the
// Ristretto adapter. The first call for a key executes the (slow) downstream and
// stores the result; subsequent calls inside the freshness TTL are served from
// the Ristretto cache without re-executing.
//
// Ristretto admits writes ASYNCHRONOUSLY through a buffer, so a value Set on one
// call may not be visible to the very next Get. This example pauses briefly
// after the priming call to let admission settle — in a real service the steady
// stream of traffic makes this a non-issue, and a dropped write merely degrades
// to a cache miss (the read-through layer re-executes), never a wrong value.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
	ristrettoadapter "github.com/byte4ever/r8e/ristretto"
)

func main() {
	ctx := context.Background()

	// The cache stores r8e.CacheEntry[V] (the freshness wrapper), keyed by string
	// (a member of the ristretto Key constraint). Only MaxSize is consumed.
	cache := ristrettoadapter.MustNew[string, r8e.CacheEntry[string]](
		r8e.CacheConfig{MaxSize: 10_000},
	)

	var downstreamCalls int

	policy := r8e.NewPolicy[string]("ristretto-demo",
		r8e.WithCache(
			cache,
			func(_ context.Context) string { return "user:42" },
			2*time.Second, // freshness TTL
		),
		r8e.WithHooks(&r8e.Hooks{
			OnCacheHit:  func() { fmt.Println("  [hook] cache HIT") },
			OnCacheMiss: func() { fmt.Println("  [hook] cache MISS") },
		}),
	)

	fetch := func(_ context.Context) (string, error) {
		downstreamCalls++

		time.Sleep(50 * time.Millisecond) // simulate a slow downstream

		return fmt.Sprintf("profile-v%d", downstreamCalls), nil
	}

	fmt.Println("=== Prime the cache (miss executes the downstream) ===")

	primed, _ := policy.Do(ctx, fetch) //nolint:errcheck // example: fetch never errors
	fmt.Printf("  call #1 -> %s\n", primed)

	// Give Ristretto's async admission buffer a moment to flush the write.
	time.Sleep(20 * time.Millisecond)

	fmt.Println("\n=== Within the freshness TTL (served from Ristretto) ===")

	for i := 2; i <= 4; i++ {
		val, _ := policy.Do(ctx, fetch) //nolint:errcheck // example: fetch never errors
		fmt.Printf("  call #%d -> %s\n", i, val)
	}

	m := policy.Metrics()
	fmt.Printf("\ndownstream executions: %d (cache hits: %d, misses: %d)\n",
		downstreamCalls, m.CacheHits, m.CacheMisses)
}
