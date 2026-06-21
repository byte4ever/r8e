// Example 24-read-through-cache: Demonstrates the read-through cache policy
// (WithCache). A fresh hit short-circuits the whole chain; past the fresh TTL a
// value lingers as a stale fallback served when revalidation fails
// (stale-if-error); failures for a never-seen key are negatively cached to
// fast-fail; and ForceRefresh busts a single call's cached read.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/byte4ever/r8e"
)

type (
	// mapCache is a tiny in-memory r8e.Cache. Real deployments use the otter or
	// ristretto adapter; note the value type is r8e.CacheEntry[string], the
	// wrapper WithCache stores so it can tell fresh, stale, and negative entries
	// apart.
	mapCache[V any] struct {
		data map[string]V
	}

	// ctxKey carries the resource id being fetched through the call context so
	// the policy's key function can read it back — the same idiom WithCoalesce
	// uses, so one key function can drive both when you pair them.
	ctxKey struct{}
)

//nolint:ireturn // generic value type V, not an interface
func (m *mapCache[V]) Get(key string) (V, bool) {
	v, ok := m.data[key]

	return v, ok
}

func (m *mapCache[V]) Set(key string, value V, _ time.Duration) {
	m.data[key] = value
}

func (m *mapCache[V]) Delete(key string) { delete(m.data, key) }

func withID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func resourceKey(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKey{}).(string); ok {
		return id
	}

	return ""
}

func main() {
	cache := &mapCache[r8e.CacheEntry[string]]{
		data: make(map[string]r8e.CacheEntry[string]),
	}

	var (
		backendCalls atomic.Int64
		fail         atomic.Bool
	)

	fetch := func(ctx context.Context) (string, error) {
		backendCalls.Add(1)

		if fail.Load() {
			return "", errors.New("downstream unavailable")
		}

		return "value of " + resourceKey(ctx), nil
	}

	policy := r8e.NewPolicy[string]("catalog",
		r8e.WithCache(
			cache, resourceKey,
			50*time.Millisecond,                     // fresh TTL: hits skip the chain
			r8e.StaleIfError(2*time.Second),         // serve stale for 2s on error
			r8e.NegativeCache(500*time.Millisecond), // cache failures briefly
		),
		// Pair with WithCoalesce(resourceKey)+WithTimeout to also collapse a
		// concurrent miss stampede into one downstream call (see example 20).
	)

	doc1 := withID(context.Background(), "doc:1")

	// --- Read-through: the second call is served from cache ---
	fmt.Println("=== Read-through ===")

	for i := 1; i <= 2; i++ {
		v, err := policy.Do(doc1, fetch)
		fmt.Printf("  call %d -> %q (err: %v)\n", i, v, err)
	}

	fmt.Printf("  backend calls: %d (second was a cache hit)\n\n", backendCalls.Load())

	// --- ForceRefresh: bypass the cached read for one call ---
	fmt.Println("=== ForceRefresh ===")
	backendCalls.Store(0)

	forced, err := policy.Do(r8e.ForceRefresh(doc1), fetch)
	fmt.Printf("  forced -> %q (err: %v), backend calls: %d\n\n",
		forced, err, backendCalls.Load())

	// --- Stale-if-error: past the fresh TTL, a failure serves the stale value ---
	fmt.Println("=== Stale-if-error ===")
	backendCalls.Store(0)

	time.Sleep(60 * time.Millisecond) // age past the 50ms fresh TTL
	fail.Store(true)                  // downstream now broken

	stale, err := policy.Do(doc1, fetch)
	fmt.Printf("  stale served -> %q, err: %v (backend tried %d time)\n\n",
		stale, err, backendCalls.Load())

	// --- Negative caching: a fresh failing key fast-fails the next call ---
	fmt.Println("=== Negative caching ===")
	backendCalls.Store(0)

	bad := withID(context.Background(), "doc:missing")

	for i := 1; i <= 2; i++ {
		_, err = policy.Do(bad, fetch)
		fmt.Printf("  call %d -> err: %v\n", i, err)
	}

	fmt.Printf("  backend calls: %d (second fast-failed from the negative entry)\n\n",
		backendCalls.Load())

	fmt.Println("=== Metrics ===")

	m := policy.Metrics()
	fmt.Printf("  hits=%d misses=%d stores=%d stale_served=%d\n",
		m.CacheHits, m.CacheMisses, m.CacheStores, m.CacheStaleServed)
}
