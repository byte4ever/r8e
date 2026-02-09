// Example 08-stale-cache: Demonstrates the standalone keyed stale cache.
// A successful call populates the cache; subsequent failures serve stale data
// for the same key.
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

// mapCache is a simple in-memory cache for demonstration purposes.
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

	stale := r8e.NewStaleCache(cache, 2*time.Second,
		r8e.OnCacheRefreshed[string, string](func(key string) {
			fmt.Printf("  [hook] cache refreshed for key=%q\n", key)
		}),
		r8e.OnStaleServed[string, string](func(key string) {
			fmt.Printf("  [hook] serving stale data for key=%q\n", key)
		}),
	)

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

	// Call 1: Success — populates cache for key "user:1".
	fmt.Println("=== Call 1: Success (populates cache) ===")

	result, err := stale.Do(ctx, "user:1", call)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Call 2: Downstream fails — stale cache serves the previous value.
	shouldFail = true

	fmt.Println("=== Call 2: Failure (served from stale cache) ===")

	result, err = stale.Do(ctx, "user:1", call)
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// Call 3: Different key, no cache — error propagates.
	fmt.Println("=== Call 3: Different key, no cache ===")

	_, err = stale.Do(ctx, "user:2", call)
	fmt.Printf("  err: %v (no cached value for this key)\n", err)
}
