// Example 01-read-through-cache: back r8e's read-through cache policy with the
// Otter adapter. The first call for a key executes the (slow) downstream and
// stores the result; subsequent calls inside the freshness TTL are served from
// the Otter cache without re-executing.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
	otteradapter "github.com/byte4ever/r8e/otter"
)

func main() {
	ctx := context.Background()

	// The cache stores r8e.CacheEntry[V] (the freshness wrapper), keyed by string
	// — the same key type r8e.WithCache uses. Only MaxSize is consumed here.
	cache := otteradapter.MustNew[string, r8e.CacheEntry[string]](
		r8e.CacheConfig{MaxSize: 10_000},
	)

	var downstreamCalls int

	policy := r8e.NewPolicy[string]("otter-demo",
		// Cache by a fixed key for the demo; a real keyFn derives the key from ctx.
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

	// First three calls within the TTL: one miss executes the downstream, the
	// rest are served from Otter.
	fmt.Println("=== Within the freshness TTL ===")

	for i := 1; i <= 3; i++ {
		val, _ := policy.Do(ctx, fetch) //nolint:errcheck // example: fetch never errors
		fmt.Printf("  call #%d -> %s\n", i, val)
	}

	// Let the entry expire, then call again: a fresh miss re-executes.
	fmt.Println("\n=== After the TTL expires ===")
	time.Sleep(2100 * time.Millisecond)

	val, _ := policy.Do(ctx, fetch) //nolint:errcheck // example: fetch never errors
	fmt.Printf("  call #4 -> %s\n", val)

	m := policy.Metrics()
	fmt.Printf("\ndownstream executions: %d (cache hits: %d, misses: %d)\n",
		downstreamCalls, m.CacheHits, m.CacheMisses)
}
