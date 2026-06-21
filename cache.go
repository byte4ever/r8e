package r8e

import "time"

type (
	// Cache is the interface that cache adapters must implement.
	// TTL is passed per Set call; the underlying cache library handles
	// expiration.
	Cache[K comparable, V any] interface {
		// Get retrieves a cached value by key. Returns the value and true if
		// found.
		Get(key K) (V, bool)
		// Set stores a value with the given TTL.
		Set(key K, value V, ttl time.Duration)
		// Delete removes a cached entry by key.
		Delete(key K)
	}

	// CacheConfig holds configuration for a cache instance. The bundled adapters
	// (otter, ristretto) consume only MaxSize; the freshness TTL is the caller's
	// concern, passed per Set call (and to [WithCache]/[NewStaleCache]), not
	// applied from this struct.
	CacheConfig struct {
		// TTL is the freshness time-to-live the caller passes to [WithCache] or
		// [NewStaleCache]. It is NOT applied by the cache adapters, which receive
		// the TTL per Set call; it is carried here only as a convenient
		// configuration value to read back.
		TTL time.Duration
		// MaxSize is the maximum number of entries the cache can hold.
		MaxSize int
	}
)
