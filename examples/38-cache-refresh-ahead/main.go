// Example 38-cache-refresh-ahead: Demonstrates refresh-ahead caching
// (WithCache + RefreshAhead). Past the refresh threshold but still within the
// fresh TTL, a read is served the current value immediately AND kicks off a
// single coalesced background reload, so a hot key keeps serving fresh hits and
// never falls through to a synchronous miss (Caffeine refreshAfterWrite). The
// detached reload is bounded by the required WithTimeout.
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
	// ristretto adapter; the value type is r8e.CacheEntry[string], the wrapper
	// WithCache stores so it can age entries against the injected clock.
	mapCache[V any] struct {
		data map[string]V
	}

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

	// Each backend call returns a new version, so a refresh is visible as a value
	// change without the caller ever seeing a miss.
	var version atomic.Int64

	fetch := func(ctx context.Context) (string, error) {
		key := resourceKey(ctx)
		if key == "" {
			return "", errors.New("missing resource id")
		}

		v := version.Add(1)

		return fmt.Sprintf("%s@v%d", key, v), nil
	}

	policy := r8e.NewPolicy[string]("catalog",
		r8e.WithCache(
			cache, resourceKey,
			100*time.Millisecond,                 // fresh TTL
			r8e.RefreshAhead(50*time.Millisecond), // reload in the background past 50ms
		),
		// RefreshAhead's detached reload needs a timeout to bound it.
		r8e.WithTimeout(time.Second),
	)

	doc := withID(context.Background(), "doc:1")

	read := func() string {
		v, err := policy.Do(doc, fetch)
		if err != nil {
			panic(err) // the fetch never fails in this example
		}

		return v
	}

	// Cold read: a miss populates the cache at v1.
	fmt.Printf("cold read    -> %q (miss, backend hit)\n", read())

	// Still fresh, before the refresh threshold: a plain hit, no reload.
	time.Sleep(20 * time.Millisecond)
	fmt.Printf("early hit    -> %q (fresh hit, no reload)\n", read())

	// Age into the refresh window [50ms, 100ms): served immediately, reload fired.
	time.Sleep(40 * time.Millisecond)
	fmt.Printf("ageing hit   -> %q (served now, refreshing in background)\n", read())

	// Give the detached reload a moment to repopulate the entry.
	time.Sleep(20 * time.Millisecond)
	fmt.Printf("refreshed hit-> %q (background reload landed; still a hit)\n", read())

	m := policy.Metrics()
	fmt.Printf("\nhits=%d misses=%d stores=%d refreshes=%d\n",
		m.CacheHits, m.CacheMisses, m.CacheStores, m.CacheRefreshes)
}
