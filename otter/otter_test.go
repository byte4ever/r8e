package otter

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
)

func newTestConfig() r8e.CacheConfig {
	return r8e.CacheConfig{
		MaxSize: 1000,
		TTL:     time.Minute,
	}
}

// ---------------------------------------------------------------------------
// New creates a valid cache without panicking
// ---------------------------------------------------------------------------

func TestNewDoesNotPanic(t *testing.T) {
	cache := MustNew[string, string](newTestConfig())
	if cache == nil {
		t.Fatal("New() returned nil")
	}
}

// ---------------------------------------------------------------------------
// Set + Get returns stored value (string key)
// ---------------------------------------------------------------------------

func TestSetGetStringKey(t *testing.T) {
	cache := MustNew[string, string](newTestConfig())

	cache.Set("hello", "world", time.Minute)

	got, ok := cache.Get("hello")
	if !ok {
		t.Fatal("Get(hello) = _, false; want _, true")
	}

	if got != "world" {
		t.Fatalf("Get(hello) = %q, want %q", got, "world")
	}
}

// ---------------------------------------------------------------------------
// Set + Get returns stored value (int key)
// ---------------------------------------------------------------------------

func TestSetGetIntKey(t *testing.T) {
	cache := MustNew[int, int](newTestConfig())

	cache.Set(42, 100, time.Minute)

	got, ok := cache.Get(42)
	if !ok {
		t.Fatal("Get(42) = _, false; want _, true")
	}

	if got != 100 {
		t.Fatalf("Get(42) = %d, want 100", got)
	}
}

// ---------------------------------------------------------------------------
// Set + Get with struct value
// ---------------------------------------------------------------------------

func TestSetGetStructValue(t *testing.T) {
	type user struct {
		Name string
		Age  int
	}

	cache := MustNew[string, user](newTestConfig())

	want := user{Name: "alice", Age: 30}
	cache.Set("user:1", want, time.Minute)

	got, ok := cache.Get("user:1")
	if !ok {
		t.Fatal("Get(user:1) = _, false; want _, true")
	}

	if got != want {
		t.Fatalf("Get(user:1) = %+v, want %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Get on missing key returns zero + false
// ---------------------------------------------------------------------------

func TestGetMissingKeyReturnsFalse(t *testing.T) {
	cache := MustNew[string, string](newTestConfig())

	got, ok := cache.Get("missing")
	if ok {
		t.Fatal("Get(missing) = _, true; want _, false")
	}

	if got != "" {
		t.Fatalf("Get(missing) = %q, want zero value", got)
	}
}

// ---------------------------------------------------------------------------
// Delete removes entry
// ---------------------------------------------------------------------------

func TestDeleteRemovesEntry(t *testing.T) {
	cache := MustNew[string, string](newTestConfig())

	cache.Set("key", "value", time.Minute)

	// Confirm it's there.
	if _, ok := cache.Get("key"); !ok {
		t.Fatal("Get(key) = _, false before Delete; want _, true")
	}

	cache.Delete("key")

	if _, ok := cache.Get("key"); ok {
		t.Fatal("Get(key) = _, true after Delete; want _, false")
	}
}

// ---------------------------------------------------------------------------
// Set overwrites existing value
// ---------------------------------------------------------------------------

func TestSetOverwritesExistingValue(t *testing.T) {
	cache := MustNew[string, string](newTestConfig())

	cache.Set("key", "old", time.Minute)
	cache.Set("key", "new", time.Minute)

	got, ok := cache.Get("key")
	if !ok {
		t.Fatal("Get(key) = _, false; want _, true")
	}

	if got != "new" {
		t.Fatalf("Get(key) = %q, want %q", got, "new")
	}
}

// ---------------------------------------------------------------------------
// Multiple distinct keys
// ---------------------------------------------------------------------------

func TestMultipleDistinctKeys(t *testing.T) {
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
		if !ok {
			t.Fatalf("Get(%q) = _, false; want _, true", tc.key)
		}

		if got != tc.want {
			t.Fatalf("Get(%q) = %d, want %d", tc.key, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Concurrent Set and Get
// ---------------------------------------------------------------------------

func TestConcurrentAccess(t *testing.T) {
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

// ---------------------------------------------------------------------------
// Interface compliance: adapter satisfies r8e.Cache
// ---------------------------------------------------------------------------

func TestInterfaceCompliance(t *testing.T) {
	var _ r8e.Cache[string, string] = MustNew[string, string](newTestConfig())
	var _ r8e.Cache[int, int] = MustNew[int, int](newTestConfig())
	var _ r8e.Cache[uint64, string] = MustNew[uint64, string](newTestConfig())
}

// ---------------------------------------------------------------------------
// Integration: works with r8e.StaleCache
// ---------------------------------------------------------------------------

func TestIntegrationWithStaleCache(t *testing.T) {
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
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}

	if result != "hello-key1" {
		t.Fatalf("Do() = %q, want %q", result, "hello-key1")
	}

	// Second call fails — should serve stale.
	result, err = sc.Do(
		context.Background(),
		"key1",
		func(_ context.Context, _ string) (string, error) {
			return "", errors.New("downstream failure")
		},
	)
	if err != nil {
		t.Fatalf("Do() error = %v, want nil (stale served)", err)
	}

	if result != "hello-key1" {
		t.Fatalf("Do() = %q, want %q (stale)", result, "hello-key1")
	}

	// Different key with no cache — error propagates.
	sentinel := errors.New("no cache")

	_, err = sc.Do(
		context.Background(),
		"unknown",
		func(_ context.Context, _ string) (string, error) {
			return "", sentinel
		},
	)

	if !errors.Is(err, sentinel) {
		t.Fatalf("Do() error = %v, want %v", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// Integration: different keys have separate entries in StaleCache
// ---------------------------------------------------------------------------

func TestIntegrationStaleCacheDifferentKeys(t *testing.T) {
	cache := MustNew[string, string](newTestConfig())
	sc := r8e.NewStaleCache(cache, time.Minute)

	// Populate two keys.
	_, _ = sc.Do(
		context.Background(),
		"k1",
		func(_ context.Context, _ string) (string, error) {
			return "v1", nil
		},
	)

	_, _ = sc.Do(
		context.Background(),
		"k2",
		func(_ context.Context, _ string) (string, error) {
			return "v2", nil
		},
	)

	// Fail both — each serves its own stale value.
	fail := func(_ context.Context, _ string) (string, error) {
		return "", errors.New("fail")
	}

	r1, err := sc.Do(context.Background(), "k1", fail)
	if err != nil || r1 != "v1" {
		t.Fatalf("Do(k1) = %q, %v; want %q, nil", r1, err, "v1")
	}

	r2, err := sc.Do(context.Background(), "k2", fail)
	if err != nil || r2 != "v2" {
		t.Fatalf("Do(k2) = %q, %v; want %q, nil", r2, err, "v2")
	}
}

// ---------------------------------------------------------------------------
// Benchmark: Set + Get
// ---------------------------------------------------------------------------

func BenchmarkSetGet(b *testing.B) {
	cache := MustNew[string, string](newTestConfig())

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			cache.Set("bench-key", "bench-value", time.Minute)
			cache.Get("bench-key")
		}
	})
}
