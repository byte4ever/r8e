package r8e

import (
	"context"
	"time"
)

type (
	// StaleCache wraps a function call with keyed stale-on-error caching.
	// On success, the result is stored in the underlying [Cache]. On failure,
	// the cached value for that key is returned if available (within TTL).
	//
	// StaleCache is a standalone wrapper — it is not part of [Policy].
	// Compose it with a Policy by calling Policy.Do inside the function
	// passed to StaleCache.Do.
	StaleCache[K comparable, V any] struct {
		cache            Cache[K, V]
		onStaleServed    func(K)
		onCacheRefreshed func(K)
		ttl              time.Duration
	}

	// StaleCacheOption configures a [StaleCache].
	StaleCacheOption[K comparable, V any] func(*StaleCache[K, V])
)

// OnStaleServed sets a callback invoked when a stale cached value is served.
func OnStaleServed[K comparable, V any](fn func(K)) StaleCacheOption[K, V] {
	return func(sc *StaleCache[K, V]) {
		sc.onStaleServed = fn
	}
}

// OnCacheRefreshed sets a callback invoked when a cache entry is refreshed.
func OnCacheRefreshed[K comparable, V any](fn func(K)) StaleCacheOption[K, V] {
	return func(sc *StaleCache[K, V]) {
		sc.onCacheRefreshed = fn
	}
}

// NewStaleCache creates a keyed stale cache backed by the given [Cache].
// The ttl determines how long cached entries remain valid.
func NewStaleCache[K comparable, V any](
	cache Cache[K, V],
	ttl time.Duration,
	opts ...StaleCacheOption[K, V],
) *StaleCache[K, V] {
	sc := &StaleCache[K, V]{
		cache: cache,
		ttl:   ttl,
	}

	for _, opt := range opts {
		opt(sc)
	}

	return sc
}

// Do executes fn with the given key. On success, the result is cached.
// On failure, a cached value is returned if one exists within TTL.
//
//nolint:ireturn,revive // generic type parameter V, not an interface; Do
// matches Policy.Do naming.
func (sc *StaleCache[K, V]) Do(
	ctx context.Context,
	key K,
	fn func(context.Context, K) (V, error),
) (V, error) {
	result, err := fn(ctx, key)
	if err == nil {
		sc.cache.Set(key, result, sc.ttl)

		if sc.onCacheRefreshed != nil {
			sc.onCacheRefreshed(key)
		}

		return result, nil
	}

	// Failure: check for a cached entry.
	if cached, ok := sc.cache.Get(key); ok {
		if sc.onStaleServed != nil {
			sc.onStaleServed(key)
		}

		return cached, nil
	}

	// No cache entry: return original error.
	var zero V

	return zero, err //nolint:wrapcheck // caller's error returned as-is
}
