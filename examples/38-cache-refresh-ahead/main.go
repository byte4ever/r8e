// Example 38-cache-refresh-ahead: Demonstrates refresh-ahead caching
// (WithCache + RefreshAhead). Past the refresh threshold but still within the
// fresh TTL, a read is served the current value immediately AND kicks off a
// single coalesced background reload, so a hot key keeps serving fresh hits and
// never falls through to a synchronous miss (Caffeine refreshAfterWrite).
//
// The problem it solves: a plain read-through cache lets a hot key expire, and
// the unlucky request that arrives at expiry eats the full backend latency (a
// "synchronous miss") — and a stampede of them can all miss at once. Refresh-ahead
// repaints the entry in the background just before it would go stale, so callers
// keep getting fast hits and the latency spike never lands on the request path.
// The reload is detached from the caller (it loses the caller's deadline), so it
// requires WithTimeout to bound it. This run walks one key through the cold miss,
// a fresh hit, a refresh-window hit that fires the background reload, and the hit
// that sees the refreshed value land — all without a single synchronous miss.
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
			100*time.Millisecond, // fresh TTL: hits within 100ms skip the backend
			// Threshold deliberately shorter than the TTL: a hit in the [50ms,
			// 100ms) tail is still served from cache but also triggers a reload, so
			// the entry is refreshed before it can expire into a synchronous miss.
			r8e.RefreshAhead(50*time.Millisecond),
		),
		// The reload runs detached and thus loses the caller's deadline; WithTimeout
		// gives it its own bound. Omitting it would panic with
		// ErrRefreshAheadWithoutTimeout once the threshold can actually fire.
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

	// Cold read at age 0: nothing cached yet, so this is the one and only
	// synchronous backend hit — it populates the entry at v1.
	fmt.Printf("cold read    -> %q (miss, backend hit)\n", read())

	// Age ~20ms: still well within the fresh TTL and before the 50ms threshold,
	// so this is a plain read-through hit — no reload, value unchanged at v1.
	time.Sleep(20 * time.Millisecond)
	fmt.Printf("early hit    -> %q (fresh hit, no reload)\n", read())

	// Age ~60ms: now inside the refresh window [50ms, 100ms). The caller is still
	// served the cached v1 immediately (no latency penalty), and a single detached
	// reload is fired in the background to repaint the entry.
	time.Sleep(40 * time.Millisecond)
	fmt.Printf("ageing hit   -> %q (served now, refreshing in background)\n", read())

	// Give the detached reload a moment to finish and write v2 back into the cache
	// before we read again — otherwise we might observe v1 before v2 lands.
	time.Sleep(20 * time.Millisecond)
	fmt.Printf("refreshed hit-> %q (background reload landed; still a hit)\n", read())

	// The tally tells the whole story: exactly one miss (the cold read), three
	// hits (every subsequent read), two stores (cold populate + refresh write),
	// and one refresh — i.e. the hot key never suffered a synchronous miss.
	m := policy.Metrics()
	fmt.Printf("\nhits=%d misses=%d stores=%d refreshes=%d\n",
		m.CacheHits, m.CacheMisses, m.CacheStores, m.CacheRefreshes)
}
