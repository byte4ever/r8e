// Package ristretto provides an adapter for the Ristretto cache library,
// implementing the r8e.Cache interface. Use it to back r8e's read-through cache
// policy — instantiate it as ristretto.MustNew[K, r8e.CacheEntry[V]](cfg) for
// r8e.WithCache / r8e.ReadThroughCache — or the standalone r8e.StaleCache.
package ristretto

import (
	"time"

	"github.com/dgraph-io/ristretto/v2"

	"github.com/byte4ever/r8e"
)

type (
	// Key is the subset of ristretto.Key types that are also comparable,
	// required by the r8e.Cache interface.
	Key interface {
		uint64 | string | byte | int | int32 | uint32 | int64
	}

	// adapter wraps a ristretto.Cache to implement r8e.Cache.
	//
	// Pattern: Adapter — exposes a third-party ristretto.Cache through the
	// r8e.Cache port the resilience layer consumes.
	adapter[K Key, V any] struct {
		cache *ristretto.Cache[K, V]
	}
)

// MustNew creates an r8e.Cache backed by a Ristretto cache.
// K must satisfy [Key] (comparable subset of ristretto key types).
// Only MaxSize from [r8e.CacheConfig] is consumed (the cache capacity); the
// freshness TTL is applied per Set call, not from the config.
// Ristretto recommends NumCounters = 10 * MaxSize for good performance.
// It panics if the underlying Ristretto cache cannot be built.
//
//nolint:ireturn,varnamelen // generic type params K,V are idiomatic in Go
func MustNew[K Key, V any](cfg r8e.CacheConfig) r8e.Cache[K, V] {
	//nolint:mnd // Ristretto recommends 10x max size for num counters and 64
	// buffer items.
	cache, err := ristretto.NewCache(&ristretto.Config[K, V]{
		NumCounters: int64(cfg.MaxSize) * 10,
		MaxCost:     int64(cfg.MaxSize),
		BufferItems: 64,
	})
	if err != nil {
		panic("r8e/ristretto: failed to build cache: " + err.Error())
	}

	return &adapter[K, V]{cache: cache}
}

// Get retrieves a cached value by key.
//
//nolint:ireturn // generic type parameter V, not an interface
func (a *adapter[K, V]) Get(key K) (V, bool) {
	return a.cache.Get(key)
}

// Set stores a value with the given TTL and a fixed cost of 1 per entry, so
// MaxSize bounds the entry count. Ristretto admits writes asynchronously and
// may drop them under buffer pressure; SetWithTTL's bool admission result is
// intentionally ignored because a dropped write degrades to a cache miss (the
// read-through layer re-executes), never a wrong value.
func (a *adapter[K, V]) Set(key K, value V, ttl time.Duration) {
	a.cache.SetWithTTL(key, value, 1, ttl)
}

// Delete removes a cached entry by key.
func (a *adapter[K, V]) Delete(key K) {
	a.cache.Del(key)
}
