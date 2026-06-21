package ristretto

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
)

func newTestConfig() r8e.CacheConfig {
	return r8e.CacheConfig{
		MaxSize: 1000,
		TTL:     time.Minute,
	}
}

// awaitGet polls Get until the key is admitted — ristretto admits writes
// asynchronously, so a deterministic poll replaces a fixed sleep.
func awaitGet[K comparable, V any](
	t *testing.T,
	c r8e.Cache[K, V],
	key K,
) V {
	t.Helper()

	var val V

	require.Eventuallyf(t, func() bool {
		v, ok := c.Get(key)
		val = v

		return ok
	}, time.Second, time.Millisecond, "key %v never admitted", key)

	return val
}

func TestNewDoesNotPanic(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())
	require.NotNil(t, cache)
}

func TestMustNewPanicsOnInvalidConfig(t *testing.T) {
	t.Parallel()

	defer func() {
		r := recover()
		require.NotNil(t, r, "MustNew should panic on an invalid config")

		msg, ok := r.(string)
		require.True(t, ok, "panic value should be a string")
		assert.Contains(t, msg, "r8e/ristretto: failed to build cache")
	}()

	_ = MustNew[string, string](r8e.CacheConfig{MaxSize: 0})
}

func TestSetGetStringKey(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())
	cache.Set("hello", "world", time.Minute)

	assert.Equal(t, "world", awaitGet(t, cache, "hello"))
}

func TestSetGetIntKey(t *testing.T) {
	t.Parallel()

	cache := MustNew[int, int](newTestConfig())
	cache.Set(42, 100, time.Minute)

	assert.Equal(t, 100, awaitGet(t, cache, 42))
}

func TestSetGetUint64Key(t *testing.T) {
	t.Parallel()

	cache := MustNew[uint64, string](newTestConfig())
	cache.Set(99, "value", time.Minute)

	assert.Equal(t, "value", awaitGet(t, cache, uint64(99)))
}

func TestGetMissingKeyReturnsFalse(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())

	got, ok := cache.Get("missing")
	assert.False(t, ok)
	assert.Empty(t, got)
}

func TestDeleteRemovesEntry(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())
	cache.Set("key", "value", time.Minute)
	_ = awaitGet(t, cache, "key")

	cache.Delete("key")

	require.Eventually(t, func() bool {
		_, ok := cache.Get("key")

		return !ok
	}, time.Second, time.Millisecond, "entry should be gone after Delete")
}

func TestSetOverwritesExistingValue(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())

	// Let the first write be admitted before overwriting, so the update is not
	// racing the initial admission (ristretto applies writes asynchronously).
	cache.Set("key", "old", time.Minute)
	require.Eventually(t, func() bool {
		v, ok := cache.Get("key")

		return ok && v == "old"
	}, time.Second, time.Millisecond, "first write should be admitted")

	cache.Set("key", "new", time.Minute)
	require.Eventually(t, func() bool {
		v, ok := cache.Get("key")

		return ok && v == "new"
	}, time.Second, time.Millisecond, "overwrite should become visible")
}

func TestConcurrentAccess(t *testing.T) {
	t.Parallel()

	cache := MustNew[int, int](newTestConfig())

	const goroutines = 50

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for i := range goroutines {
		go func() {
			defer wg.Done()

			cache.Set(i, i*10, time.Minute)
			cache.Get(i)
		}()
	}

	wg.Wait()
}

func TestInterfaceCompliance(t *testing.T) {
	t.Parallel()

	var _ r8e.Cache[string, string] = MustNew[string, string](newTestConfig())
	var _ r8e.Cache[int, int] = MustNew[int, int](newTestConfig())
	var _ r8e.Cache[uint64, string] = MustNew[uint64, string](newTestConfig())
}

func TestIntegrationWithStaleCache(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())
	sc := r8e.NewStaleCache(cache, time.Minute)

	// First call succeeds — populates cache.
	result, err := sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, key string) (string, error) {
			return "hello-" + key, nil
		},
	)
	require.NoError(t, err)
	assert.Equal(t, "hello-key1", result)

	// Wait for ristretto's async admission before the stale-serve path.
	_ = awaitGet(t, cache, "key1")

	// Second call fails — should serve stale.
	result, err = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("downstream failure")
		},
	)
	require.NoError(t, err, "stale value should be served")
	assert.Equal(t, "hello-key1", result)

	// Unknown key with no cache — error propagates.
	sentinel := errors.New("no cache")

	_, err = sc.Do(
		context.Background(),
		"unknown",
		func(_ context.Context, _ string) (string, error) {
			return "", sentinel
		},
	)
	require.ErrorIs(t, err, sentinel)
}

func BenchmarkSetGet(b *testing.B) {
	cache := MustNew[string, string](newTestConfig())

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Set("bench-key", "bench-value", time.Minute)
			cache.Get("bench-key")
		}
	})
}
