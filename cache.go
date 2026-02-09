package r8e

import (
	"fmt"
	"os"
	"time"

	json "github.com/goccy/go-json"
)

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

	cacheConfigFile struct {
		Caches map[string]cacheConfigJSON `json:"caches"`
	}

	cacheConfigJSON struct {
		Options map[string]any `json:"options,omitempty"`
		TTL     string         `json:"ttl"`
		MaxSize int            `json:"max_size"`
	}
)

// LoadCacheConfig reads a JSON configuration file and returns the CacheConfig
// for the named cache entry.
func LoadCacheConfig(path, name string) (CacheConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return CacheConfig{}, fmt.Errorf("r8e: read cache config: %w", err)
	}

	var cfg cacheConfigFile

	if err = json.Unmarshal(data, &cfg); err != nil {
		return CacheConfig{}, fmt.Errorf("r8e: parse cache config: %w", err)
	}

	raw, ok := cfg.Caches[name]
	if !ok {
		return CacheConfig{}, fmt.Errorf(
			"r8e: cache %q not found in config",
			name,
		)
	}

	cc := CacheConfig{
		Options: raw.Options,
		MaxSize: raw.MaxSize,
	}

	if raw.TTL != "" {
		ttl, ttlErr := time.ParseDuration(raw.TTL)
		if ttlErr != nil {
			return CacheConfig{}, fmt.Errorf(
				"r8e: cache %q: ttl: %w",
				name,
				ttlErr,
			)
		}

		cc.TTL = ttl
	}

	return cc, nil
}
