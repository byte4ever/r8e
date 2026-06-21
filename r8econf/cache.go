package r8econf

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/byte4ever/r8e"
)

type (
	cacheConfigFile struct {
		Caches map[string]cacheConfigJSON `json:"caches"`
	}

	cacheConfigJSON struct {
		TTL     string `json:"ttl"`
		MaxSize int    `json:"max_size"`
	}
)

// LoadCacheConfig reads a JSON configuration file and returns the
// [r8e.CacheConfig] for the named cache entry.
func LoadCacheConfig(path, name string) (r8e.CacheConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return r8e.CacheConfig{}, fmt.Errorf("r8e: read cache config: %w", err)
	}

	var cfg cacheConfigFile
	if err = json.Unmarshal(data, &cfg); err != nil {
		return r8e.CacheConfig{}, fmt.Errorf("r8e: parse cache config: %w", err)
	}

	raw, ok := cfg.Caches[name]
	if !ok {
		return r8e.CacheConfig{}, fmt.Errorf(
			"r8e: cache %q not found in config",
			name,
		)
	}

	cacheCfg := r8e.CacheConfig{
		MaxSize: raw.MaxSize,
	}

	if raw.TTL != "" {
		ttl, ttlErr := time.ParseDuration(raw.TTL)
		if ttlErr != nil {
			return r8e.CacheConfig{}, fmt.Errorf(
				"r8e: cache %q: ttl: %w",
				name,
				ttlErr,
			)
		}

		cacheCfg.TTL = ttl
	}

	return cacheCfg, nil
}
