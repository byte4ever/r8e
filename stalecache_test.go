package r8e_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testCache is a simple in-memory cache for testing.
type testCache[K comparable, V any] struct {
	mu   sync.RWMutex
	data map[K]V
}

func newTestCache[K comparable, V any]() *testCache[K, V] {
	return &testCache[K, V]{data: make(map[K]V)}
}

func (c *testCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	v, ok := c.data[key]

	return v, ok
}

func (c *testCache[K, V]) Set(key K, value V, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data[key] = value
}

func (c *testCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.data, key)
}

// ---------------------------------------------------------------------------
// First call succeeds -> cached
// ---------------------------------------------------------------------------

func TestStaleCacheFirstCallSucceedsCachesResult(t *testing.T) {
	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(cache, time.Minute)

	result, err := sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, key string) (string, error) {
			return "hello-" + key, nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "hello-key1", result)

	// Verify cache was populated.
	v, ok := cache.Get("key1")
	require.True(t, ok)
	require.Equal(t, "hello-key1", v)
}

// ---------------------------------------------------------------------------
// Second call fails, cache available -> returns cached value
// ---------------------------------------------------------------------------

func TestStaleCacheFailWithCacheReturnsCachedValue(t *testing.T) {
	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(cache, time.Minute)

	// First call succeeds, populating cache.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "cached-value", nil
		},
	)

	// Second call fails.
	result, err := sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("temporary failure")
		},
	)
	require.NoError(t, err)
	require.Equal(t, "cached-value", result)
}

// ---------------------------------------------------------------------------
// First call fails, no cache -> returns error
// ---------------------------------------------------------------------------

func TestStaleCacheFirstCallFailsNoCacheReturnsError(t *testing.T) {
	cache := newTestCache[string, int]()
	sc := r8e.NewStaleCache(cache, time.Minute)

	sentinel := errors.New("first call failure")

	result, err := sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (int, error) {
			return 0, sentinel
		},
	)

	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 0, result)
}

// ---------------------------------------------------------------------------
// Different keys have separate cache entries
// ---------------------------------------------------------------------------

func TestStaleCacheDifferentKeysAreSeparate(t *testing.T) {
	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(cache, time.Minute)

	// Populate cache for key1.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "value1", nil
		},
	)

	// Populate cache for key2.
	_, _ = sc.Do(
		context.Background(),
		"key2",
		func(_ context.Context, _ string) (string, error) {
			return "value2", nil
		},
	)

	// Fail for key1 — should get value1.
	result, err := sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("fail")
		},
	)
	require.NoError(t, err)
	require.Equal(t, "value1", result)

	// Fail for key2 — should get value2.
	result, err = sc.Do(
		context.Background(),
		"key2",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("fail")
		},
	)
	require.NoError(t, err)
	require.Equal(t, "value2", result)

	// Fail for key3 — no cache entry, error propagates.
	sentinel := errors.New("no cache")
	_, err = sc.Do(
		context.Background(),
		"key3",
		func(_ context.Context, _ string) (string, error) {
			return "", sentinel
		},
	)

	require.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// Concurrent access (multiple goroutines calling Do)
// ---------------------------------------------------------------------------

func TestStaleCacheConcurrentAccess(t *testing.T) {
	cache := newTestCache[string, int]()
	sc := r8e.NewStaleCache(cache, time.Minute)

	// Seed the cache.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (int, error) {
			return 42, nil
		},
	)

	const goroutines = 100

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()

			// All calls fail — should serve stale value 42.
			result, err := sc.Do(
				context.Background(),
				"key1",
				func(_ context.Context, _ string) (int, error) {
					return 0, errors.New("fail")
				},
			)
			if !assert.NoError(t, err) {
				return
			}

			assert.Equal(t, 42, result)
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Hook emission: OnCacheRefreshed on success
// ---------------------------------------------------------------------------

func TestStaleCacheHookOnCacheRefreshed(t *testing.T) {
	var refreshedKeys []string

	var mu sync.Mutex

	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(cache, time.Minute,
		r8e.OnCacheRefreshed[string, string](func(key string) {
			mu.Lock()
			refreshedKeys = append(refreshedKeys, key)
			mu.Unlock()
		}),
	)

	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "ok", nil
		},
	)

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, []string{"key1"}, refreshedKeys)
}

// ---------------------------------------------------------------------------
// Hook emission: OnStaleServed on stale serve
// ---------------------------------------------------------------------------

func TestStaleCacheHookOnStaleServed(t *testing.T) {
	var servedKeys []string

	var mu sync.Mutex

	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(cache, time.Minute,
		r8e.OnStaleServed[string, string](func(key string) {
			mu.Lock()
			servedKeys = append(servedKeys, key)
			mu.Unlock()
		}),
	)

	// Seed cache.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "value", nil
		},
	)

	// Fail -> serve stale.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("fail")
		},
	)

	mu.Lock()
	defer mu.Unlock()

	require.Equal(t, []string{"key1"}, servedKeys)
}

// ---------------------------------------------------------------------------
// Hook NOT emitted: OnStaleServed NOT fired on success
// ---------------------------------------------------------------------------

func TestStaleCacheHookOnStaleServedNotFiredOnSuccess(t *testing.T) {
	var servedCount atomic.Int64

	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(
		cache,
		time.Minute,
		r8e.OnStaleServed[string, string](
			func(_ string) { servedCount.Add(1) },
		),
	)

	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "ok", nil
		},
	)

	require.Zero(t, servedCount.Load())
}

// ---------------------------------------------------------------------------
// Hook NOT emitted: OnCacheRefreshed NOT fired on failure
// ---------------------------------------------------------------------------

func TestStaleCacheHookOnCacheRefreshedNotFiredOnFailure(t *testing.T) {
	var refreshedCount atomic.Int64

	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(
		cache,
		time.Minute,
		r8e.OnCacheRefreshed[string, string](
			func(_ string) { refreshedCount.Add(1) },
		),
	)

	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("fail")
		},
	)

	require.Zero(t, refreshedCount.Load())
}

// ---------------------------------------------------------------------------
// Nil hooks don't panic
// ---------------------------------------------------------------------------

func TestStaleCacheNilHooksDoNotPanic(t *testing.T) {
	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(cache, time.Minute) // No hooks set.

	// Success path.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "ok", nil
		},
	)

	// Failure path with stale serve.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("fail")
		},
	)
	// If we get here without panicking, the test passes.
}

// ---------------------------------------------------------------------------
// Success after stale serve refreshes cache
// ---------------------------------------------------------------------------

func TestStaleCacheSuccessAfterStaleRefreshesCache(t *testing.T) {
	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(cache, time.Minute)

	// Seed cache.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "old", nil
		},
	)

	// Fail -> serve stale.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("fail")
		},
	)

	// Succeed again -> should refresh cache.
	result, err := sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "new", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "new", result)

	// Verify cache was updated.
	v, ok := cache.Get("key1")
	require.True(t, ok)
	require.Equal(t, "new", v)
}

// ---------------------------------------------------------------------------
// Key is passed through to the function
// ---------------------------------------------------------------------------

func TestStaleCacheKeyPassedToFunction(t *testing.T) {
	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(cache, time.Minute)

	var receivedKey string

	_, _ = sc.Do(
		context.Background(),
		"my-key",
		func(_ context.Context, key string) (string, error) {
			receivedKey = key

			return "ok", nil
		},
	)

	require.Equal(t, "my-key", receivedKey)
}

// ---------------------------------------------------------------------------
// Benchmark: concurrent Do calls that hit cache
// ---------------------------------------------------------------------------

func BenchmarkStaleCacheHit(b *testing.B) {
	cache := newTestCache[string, string]()
	sc := r8e.NewStaleCache(cache, time.Hour)

	// Seed the cache.
	_, _ = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "cached", nil
		},
	)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, _ = sc.Do(
				context.Background(),
				"key1",
				func(_ context.Context, _ string) (string, error) {
					return "", errors.New("fail")
				},
			)
		}
	})
}
