package r8e_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
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
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	if result != "hello-key1" {
		t.Fatalf("Do() = %q, want %q", result, "hello-key1")
	}

	// Verify cache was populated.
	if v, ok := cache.Get("key1"); !ok || v != "hello-key1" {
		t.Fatalf("cache.Get(key1) = %q, %v; want %q, true", v, ok, "hello-key1")
	}
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
	if err != nil {
		t.Fatalf("Do() error = %v, want nil (stale served)", err)
	}

	if result != "cached-value" {
		t.Fatalf("Do() = %q, want %q", result, "cached-value")
	}
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

	if !errors.Is(err, sentinel) {
		t.Fatalf("Do() error = %v, want %v", err, sentinel)
	}

	if result != 0 {
		t.Fatalf("Do() = %d, want 0", result)
	}
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
	if err != nil {
		t.Fatalf("Do(key1) error = %v, want nil", err)
	}

	if result != "value1" {
		t.Fatalf("Do(key1) = %q, want %q", result, "value1")
	}

	// Fail for key2 — should get value2.
	result, err = sc.Do(
		context.Background(),
		"key2",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("fail")
		},
	)
	if err != nil {
		t.Fatalf("Do(key2) error = %v, want nil", err)
	}

	if result != "value2" {
		t.Fatalf("Do(key2) = %q, want %q", result, "value2")
	}

	// Fail for key3 — no cache entry, error propagates.
	sentinel := errors.New("no cache")
	_, err = sc.Do(
		context.Background(),
		"key3",
		func(_ context.Context, _ string) (string, error) {
			return "", sentinel
		},
	)

	if !errors.Is(err, sentinel) {
		t.Fatalf("Do(key3) error = %v, want %v", err, sentinel)
	}
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
			if err != nil {
				t.Errorf("Do() error = %v, want nil (stale served)", err)

				return
			}

			if result != 42 {
				t.Errorf("Do() = %d, want 42", result)
			}
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

	if len(refreshedKeys) != 1 || refreshedKeys[0] != "key1" {
		t.Fatalf("OnCacheRefreshed keys = %v, want [key1]", refreshedKeys)
	}
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

	if len(servedKeys) != 1 || servedKeys[0] != "key1" {
		t.Fatalf("OnStaleServed keys = %v, want [key1]", servedKeys)
	}
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

	if got := servedCount.Load(); got != 0 {
		t.Fatalf("OnStaleServed called %d times on success, want 0", got)
	}
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

	if got := refreshedCount.Load(); got != 0 {
		t.Fatalf("OnCacheRefreshed called %d times on failure, want 0", got)
	}
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
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	if result != "new" {
		t.Fatalf("Do() = %q, want %q", result, "new")
	}

	// Verify cache was updated.
	if v, ok := cache.Get("key1"); !ok || v != "new" {
		t.Fatalf("cache.Get(key1) = %q, %v; want %q, true", v, ok, "new")
	}
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

	if receivedKey != "my-key" {
		t.Fatalf("fn received key = %q, want %q", receivedKey, "my-key")
	}
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
