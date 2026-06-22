// Example 08-stale-cache: Demonstrates the standalone keyed stale-on-error
// cache (StaleCache). The problem it solves: a downstream blip shouldn't take
// your service down with it. On success, StaleCache records the result per key;
// on a later failure for that same key, it serves the last-known-good value
// instead of propagating the error — degrading gracefully to slightly-stale
// data rather than no data. A key never seen before has nothing to fall back
// on, so its error surfaces normally.
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

// mapCache is a deliberately trivial in-memory backend that satisfies the
// Cache[K, V] interface (Get/Set/Delete). It exists only to show the contract
// StaleCache depends on — in production you'd plug in a real adapter (otter,
// ristretto) with eviction and bounded memory; this map has neither.
type mapCache[K comparable, V any] struct {
	data map[K]V
}

//nolint:ireturn // generic type parameter V, not an interface
func (m *mapCache[K, V]) Get(key K) (V, bool) {
	v, ok := m.data[key]
	return v, ok
}

func (m *mapCache[K, V]) Set(key K, value V, _ time.Duration) {
	m.data[key] = value
}

func (m *mapCache[K, V]) Delete(key K) {
	delete(m.data, key)
}

func main() {
	ctx := context.Background()

	cache := &mapCache[string, string]{data: make(map[string]string)}

	// 2s TTL bounds how stale a fallback may be: entries older than this are no
	// longer eligible to be served on error. The two hooks make the cache's
	// decisions observable — OnCacheRefreshed on a successful write,
	// OnStaleServed when a failure is rescued by a cached value.
	stale := r8e.NewStaleCache(cache, 2*time.Second,
		r8e.OnCacheRefreshed[string, string](func(key string) {
			fmt.Printf("  [hook] cache refreshed for key=%q\n", key)
		}),
		r8e.OnStaleServed[string, string](func(key string) {
			fmt.Printf("  [hook] serving stale data for key=%q\n", key)
		}),
	)

	// A flag we flip mid-run to simulate the downstream going from healthy to
	// down, so we can watch the same key transition from fresh to stale-served.
	shouldFail := false

	call := func(_ context.Context, key string) (string, error) {
		if shouldFail {
			return "", errors.New("downstream unavailable")
		}

		return fmt.Sprintf(
			"fresh data for %s at %v",
			key,
			time.Now().Format("15:04:05.000"),
		), nil
	}

	// Call 1: happy path. The call succeeds, so StaleCache stores the result
	// under "user:1" — this write is what makes a later fallback possible.
	fmt.Println("=== Call 1: Success (populates cache) ===")

	result, err := stale.Do(ctx, "user:1", call)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Call 2: now the downstream is down. The raw call returns an error, but
	// because "user:1" was cached in call 1 the stale value is served and the
	// caller still gets a (nil-error) result — graceful degradation in action.
	shouldFail = true

	fmt.Println("=== Call 2: Failure (served from stale cache) ===")

	result, err = stale.Do(ctx, "user:1", call)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Call 3: same outage, but a brand-new key. Per-key isolation means
	// "user:2" has nothing cached to fall back on, so the original error
	// propagates — stale serving is not a blanket error suppressor.
	fmt.Println("=== Call 3: Different key, no cache ===")

	_, err = stale.Do(ctx, "user:2", call)
	fmt.Printf("  err: %v (no cached value for this key)\n", err)
}
