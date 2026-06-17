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

	// CacheConfig holds configuration for a cache instance.
	CacheConfig struct {
		// Options holds adapter-specific settings (e.g.,
		// "reset_ttl_on_access").
		Options map[string]any
		// TTL is the time-to-live for cached entries.
		TTL time.Duration
		// MaxSize is the maximum number of entries the cache can hold.
		MaxSize int
	}
)
