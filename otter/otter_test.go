package otter

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

func TestNewDoesNotPanic(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())
	require.NotNil(t, cache)
}

func TestMustNewPanicsOnInvalidConfig(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() {
		_ = MustNew[string, string](r8e.CacheConfig{MaxSize: 0})
	})
}

func TestSetGetStringKey(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())
	cache.Set("hello", "world", time.Minute)

	got, ok := cache.Get("hello")
	require.True(t, ok)
	assert.Equal(t, "world", got)
}

func TestSetGetIntKey(t *testing.T) {
	t.Parallel()

	cache := MustNew[int, int](newTestConfig())
	cache.Set(42, 100, time.Minute)

	got, ok := cache.Get(42)
	require.True(t, ok)
	assert.Equal(t, 100, got)
}

func TestSetGetStructValue(t *testing.T) {
	t.Parallel()

	type user struct {
		Name string
		Age  int
	}

	cache := MustNew[string, user](newTestConfig())

	want := user{Name: "alice", Age: 30}
	cache.Set("user:1", want, time.Minute)

	got, ok := cache.Get("user:1")
	require.True(t, ok)
	assert.Equal(t, want, got)
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

	_, ok := cache.Get("key")
	require.True(t, ok, "entry should be present before Delete")

	cache.Delete("key")

	_, ok = cache.Get("key")
	assert.False(t, ok, "entry should be gone after Delete")
}

func TestSetOverwritesExistingValue(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())
	cache.Set("key", "old", time.Minute)
	cache.Set("key", "new", time.Minute)

	got, ok := cache.Get("key")
	require.True(t, ok)
	assert.Equal(t, "new", got)
}

func TestMultipleDistinctKeys(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, int](newTestConfig())
	cache.Set("a", 1, time.Minute)
	cache.Set("b", 2, time.Minute)
	cache.Set("c", 3, time.Minute)

	for _, tc := range []struct {
		key  string
		want int
	}{
		{"a", 1},
		{"b", 2},
		{"c", 3},
	} {
		got, ok := cache.Get(tc.key)
		require.Truef(t, ok, "Get(%q) missing", tc.key)
		assert.Equalf(t, tc.want, got, "Get(%q)", tc.key)
	}
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
			// Read back — may or may not be set yet from other goroutines.
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

	// Different key with no cache — error propagates.
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

func TestIntegrationStaleCacheDifferentKeys(t *testing.T) {
	t.Parallel()

	cache := MustNew[string, string](newTestConfig())
	sc := r8e.NewStaleCache(cache, time.Minute)

	_, _ = sc.Do(
		context.Background(),
		"k1",
		func(_ context.Context, _ string) (string, error) { return "v1", nil },
	)
	_, _ = sc.Do(
		context.Background(),
		"k2",
		func(_ context.Context, _ string) (string, error) { return "v2", nil },
	)

	fail := func(_ context.Context, _ string) (string, error) {
		return "", errors.New("fail")
	}

	r1, err := sc.Do(context.Background(), "k1", fail)
	require.NoError(t, err)
	assert.Equal(t, "v1", r1)

	r2, err := sc.Do(context.Background(), "k2", fail)
	require.NoError(t, err)
	assert.Equal(t, "v2", r2)
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
