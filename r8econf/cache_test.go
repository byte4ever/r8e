package r8econf

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadCacheConfigValid(t *testing.T) {
	cfg, err := LoadCacheConfig("../testdata/cache.json", "users")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, cfg.TTL)
	assert.Equal(t, 1000, cfg.MaxSize)
}

func TestLoadCacheConfigReadError(t *testing.T) {
	_, err := LoadCacheConfig("../testdata/nonexistent.json", "users")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "r8e: read cache config")
}

func TestLoadCacheConfigParseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(path, []byte(`{not json}`), 0o600))

	_, err := LoadCacheConfig(path, "users")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "r8e: parse cache config")
}

func TestLoadCacheConfigNameNotFound(t *testing.T) {
	_, err := LoadCacheConfig("../testdata/cache.json", "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), `cache "missing" not found`)
}

func TestLoadCacheConfigInvalidTTL(t *testing.T) {
	_, err := LoadCacheConfig("../testdata/cache.json", "bad-ttl")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ttl")
}
