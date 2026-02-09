package ristretto

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
)

// waitForAdmission gives ristretto time to process buffered writes.
func waitForAdmission() {
	//nolint:mnd // small sleep for ristretto's async admission policy
	time.Sleep(10 * time.Millisecond)
}

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
	waitForAdmission()

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
	waitForAdmission()

	got, ok := cache.Get(42)
	if !ok {
		t.Fatal("Get(42) = _, false; want _, true")
	}

	if got != 100 {
		t.Fatalf("Get(42) = %d, want 100", got)
	}
}

// ---------------------------------------------------------------------------
// Set + Get returns stored value (uint64 key)
// ---------------------------------------------------------------------------

func TestSetGetUint64Key(t *testing.T) {
	cache := MustNew[uint64, string](newTestConfig())

	cache.Set(99, "value", time.Minute)
	waitForAdmission()

	got, ok := cache.Get(99)
	if !ok {
		t.Fatal("Get(99) = _, false; want _, true")
	}

	if got != "value" {
		t.Fatalf("Get(99) = %q, want %q", got, "value")
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
	waitForAdmission()

	// Confirm it's there.
	if _, ok := cache.Get("key"); !ok {
		t.Fatal("Get(key) = _, false before Delete; want _, true")
	}

	cache.Delete("key")
	waitForAdmission()

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
	waitForAdmission()

	cache.Set("key", "new", time.Minute)
	waitForAdmission()

	got, ok := cache.Get("key")
	if !ok {
		t.Fatal("Get(key) = _, false; want _, true")
	}

	if got != "new" {
		t.Fatalf("Get(key) = %q, want %q", got, "new")
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

	// Wait for ristretto's async admission before the stale-serve path.
	waitForAdmission()

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

	// Third call with unknown key fails — error propagates.
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
