package r8e

import (
	"context"
	"sync/atomic"
	"time"
)

// Pattern: Stale Cache — serves last-known-good value on failure;
// lock-free via atomic pointer swap for cached entries.

// cacheEntry holds a cached value and the time it was stored.
type cacheEntry[T any] struct {
	value    T
	storedAt time.Time
}

// StaleCache caches successful results and serves them when subsequent calls fail.
type StaleCache[T any] struct {
	ttl          time.Duration
	clock        Clock
	hooks        *Hooks
	cached       atomic.Pointer[cacheEntry[T]]
	servingStale atomic.Bool
	staleAgeNs   atomic.Int64
}

// NewStaleCache creates a stale cache with the given TTL.
func NewStaleCache[T any](ttl time.Duration, clock Clock, hooks *Hooks) *StaleCache[T] {
	return &StaleCache[T]{
		ttl:   ttl,
		clock: clock,
		hooks: hooks,
	}
}

// Do executes fn. On success, caches the result. On failure, returns the cached
// value if one exists and is within TTL.
func (sc *StaleCache[T]) Do(ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	result, err := fn(ctx)

	if err == nil {
		// Success: atomically swap in new cache entry.
		entry := &cacheEntry[T]{
			value:    result,
			storedAt: sc.clock.Now(),
		}
		sc.cached.Store(entry)
		sc.servingStale.Store(false)
		sc.staleAgeNs.Store(0)
		sc.hooks.emitCacheRefreshed()
		return result, nil
	}

	// Failure: check for a cached entry.
	entry := sc.cached.Load()
	if entry != nil {
		age := sc.clock.Since(entry.storedAt)
		if age <= sc.ttl {
			sc.servingStale.Store(true)
			sc.staleAgeNs.Store(int64(age))
			sc.hooks.emitStaleServed(age)
			return entry.value, nil
		}
	}

	// No cache or expired: return original error.
	var zero T
	sc.servingStale.Store(false)
	sc.staleAgeNs.Store(0)
	return zero, err
}

// ServingStale reports whether the last Do call served a stale cached value.
func (sc *StaleCache[T]) ServingStale() bool {
	return sc.servingStale.Load()
}

// StaleAge returns the age of the cached value, or 0 if not serving stale.
func (sc *StaleCache[T]) StaleAge() time.Duration {
	return time.Duration(sc.staleAgeNs.Load())
}
