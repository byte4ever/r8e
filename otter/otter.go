// Package otter provides an adapter for the Otter cache library,
// implementing the r8e.Cache interface for use with r8e.StaleCache.
package otter

import (
	"time"

	"github.com/maypok86/otter"

	"github.com/byte4ever/r8e"
)

// adapter wraps an otter.CacheWithVariableTTL to implement r8e.Cache.
type adapter[K comparable, V any] struct {
	cache otter.CacheWithVariableTTL[K, V]
}

// MustNew creates an r8e.Cache backed by an Otter cache with per-entry TTL
// support.
// MaxSize from [r8e.CacheConfig] configures the underlying cache capacity.
// It panics if the underlying Otter cache cannot be built.
//
//nolint:ireturn,varnamelen // generic type params K,V are idiomatic in Go
func MustNew[K comparable, V any](cfg r8e.CacheConfig) r8e.Cache[K, V] {
	cache, err := otter.MustBuilder[K, V](cfg.MaxSize).
		WithVariableTTL().
		Build()
	if err != nil {
		panic("r8e/otter: failed to build cache: " + err.Error())
	}

	return &adapter[K, V]{cache: cache}
}

// Get retrieves a cached value by key.
//
//nolint:ireturn // generic type parameter V, not an interface
func (a *adapter[K, V]) Get(key K) (V, bool) {
	return a.cache.Get(key)
}

// Set stores a value with the given TTL.
func (a *adapter[K, V]) Set(key K, value V, ttl time.Duration) {
	a.cache.Set(key, value, ttl)
}

// Delete removes a cached entry by key.
func (a *adapter[K, V]) Delete(key K) {
	a.cache.Delete(key)
}
