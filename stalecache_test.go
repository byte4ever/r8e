package r8e

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// staleClock is a fake clock that lets tests control time.
type staleClock struct {
	now    time.Time
	offset time.Duration
}

func newStaleClock() *staleClock {
	return &staleClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *staleClock) Now() time.Time                { return c.now.Add(c.offset) }
func (c *staleClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }
func (c *staleClock) NewTimer(d time.Duration) Timer { return &fakeTimer{} }

func (c *staleClock) advance(d time.Duration) { c.offset += d }

// ---------------------------------------------------------------------------
// First call succeeds -> cached, ServingStale=false
// ---------------------------------------------------------------------------

func TestStaleCacheFirstCallSucceedsCachesResult(t *testing.T) {
	clk := newStaleClock()
	sc := NewStaleCache[string](time.Minute, clk, &Hooks{})

	result, err := sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "hello", nil
	})

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "hello" {
		t.Fatalf("Do() = %q, want %q", result, "hello")
	}
	if sc.ServingStale() {
		t.Fatal("ServingStale() = true after success, want false")
	}
	if sc.StaleAge() != 0 {
		t.Fatalf("StaleAge() = %v, want 0", sc.StaleAge())
	}
}

// ---------------------------------------------------------------------------
// Second call fails, cache within TTL -> returns cached value
// ---------------------------------------------------------------------------

func TestStaleCacheFailWithinTTLReturnsCachedValue(t *testing.T) {
	clk := newStaleClock()
	sc := NewStaleCache[string](time.Minute, clk, &Hooks{})

	// First call succeeds, populating cache.
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "cached-value", nil
	})

	// Advance time but stay within TTL.
	clk.advance(30 * time.Second)

	// Second call fails.
	result, err := sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("temporary failure")
	})

	if err != nil {
		t.Fatalf("Do() error = %v, want nil (stale served)", err)
	}
	if result != "cached-value" {
		t.Fatalf("Do() = %q, want %q", result, "cached-value")
	}
	if !sc.ServingStale() {
		t.Fatal("ServingStale() = false, want true")
	}
	if sc.StaleAge() != 30*time.Second {
		t.Fatalf("StaleAge() = %v, want %v", sc.StaleAge(), 30*time.Second)
	}
}

// ---------------------------------------------------------------------------
// Cache expired -> returns error
// ---------------------------------------------------------------------------

func TestStaleCacheExpiredReturnsError(t *testing.T) {
	clk := newStaleClock()
	sc := NewStaleCache[string](time.Minute, clk, &Hooks{})

	// First call succeeds.
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "cached-value", nil
	})

	// Advance beyond TTL.
	clk.advance(2 * time.Minute)

	sentinel := errors.New("failure after expiry")

	result, err := sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("Do() error = %v, want %v", err, sentinel)
	}
	if result != "" {
		t.Fatalf("Do() = %q, want zero value", result)
	}
	if sc.ServingStale() {
		t.Fatal("ServingStale() = true after expired cache, want false")
	}
}

// ---------------------------------------------------------------------------
// First call fails, no cache -> returns error
// ---------------------------------------------------------------------------

func TestStaleCacheFirstCallFailsNoCacheReturnsError(t *testing.T) {
	clk := newStaleClock()
	sc := NewStaleCache[int](time.Minute, clk, &Hooks{})

	sentinel := errors.New("first call failure")

	result, err := sc.Do(context.Background(), func(_ context.Context) (int, error) {
		return 0, sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("Do() error = %v, want %v", err, sentinel)
	}
	if result != 0 {
		t.Fatalf("Do() = %d, want 0", result)
	}
	if sc.ServingStale() {
		t.Fatal("ServingStale() = true with no cache, want false")
	}
}

// ---------------------------------------------------------------------------
// Concurrent access (multiple goroutines calling Do)
// ---------------------------------------------------------------------------

func TestStaleCacheConcurrentAccess(t *testing.T) {
	clk := newStaleClock()
	sc := NewStaleCache[int](time.Minute, clk, &Hooks{})

	// Seed the cache.
	_, _ = sc.Do(context.Background(), func(_ context.Context) (int, error) {
		return 42, nil
	})

	clk.advance(10 * time.Second)

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()

			// Some calls succeed, some fail — all should be safe under race detector.
			result, err := sc.Do(context.Background(), func(_ context.Context) (int, error) {
				return 0, errors.New("fail")
			})

			// Should serve stale since cache is within TTL.
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
	var refreshedCount atomic.Int64
	hooks := &Hooks{
		OnCacheRefreshed: func() { refreshedCount.Add(1) },
	}

	clk := newStaleClock()
	sc := NewStaleCache[string](time.Minute, clk, hooks)

	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})

	if got := refreshedCount.Load(); got != 1 {
		t.Fatalf("OnCacheRefreshed called %d times, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Hook emission: OnStaleServed(age) on stale serve
// ---------------------------------------------------------------------------

func TestStaleCacheHookOnStaleServed(t *testing.T) {
	var servedAge time.Duration
	var servedCount atomic.Int64
	hooks := &Hooks{
		OnStaleServed: func(age time.Duration) {
			servedAge = age
			servedCount.Add(1)
		},
	}

	clk := newStaleClock()
	sc := NewStaleCache[string](time.Minute, clk, hooks)

	// Seed cache.
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "value", nil
	})

	clk.advance(20 * time.Second)

	// Fail -> serve stale.
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("fail")
	})

	if got := servedCount.Load(); got != 1 {
		t.Fatalf("OnStaleServed called %d times, want 1", got)
	}
	if servedAge != 20*time.Second {
		t.Fatalf("OnStaleServed age = %v, want %v", servedAge, 20*time.Second)
	}
}

// ---------------------------------------------------------------------------
// Hook NOT emitted: OnStaleServed NOT fired on success
// ---------------------------------------------------------------------------

func TestStaleCacheHookOnStaleServedNotFiredOnSuccess(t *testing.T) {
	var servedCount atomic.Int64
	hooks := &Hooks{
		OnStaleServed: func(_ time.Duration) { servedCount.Add(1) },
	}

	clk := newStaleClock()
	sc := NewStaleCache[string](time.Minute, clk, hooks)

	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})

	if got := servedCount.Load(); got != 0 {
		t.Fatalf("OnStaleServed called %d times on success, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Hook NOT emitted: OnCacheRefreshed NOT fired on failure
// ---------------------------------------------------------------------------

func TestStaleCacheHookOnCacheRefreshedNotFiredOnFailure(t *testing.T) {
	var refreshedCount atomic.Int64
	hooks := &Hooks{
		OnCacheRefreshed: func() { refreshedCount.Add(1) },
	}

	clk := newStaleClock()
	sc := NewStaleCache[string](time.Minute, clk, hooks)

	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("fail")
	})

	if got := refreshedCount.Load(); got != 0 {
		t.Fatalf("OnCacheRefreshed called %d times on failure, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Nil hooks don't panic
// ---------------------------------------------------------------------------

func TestStaleCacheNilHooksDoNotPanic(t *testing.T) {
	clk := newStaleClock()
	sc := NewStaleCache[string](time.Minute, clk, &Hooks{})

	// Success path (emits OnCacheRefreshed with nil hook).
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "ok", nil
	})

	clk.advance(10 * time.Second)

	// Failure path with stale serve (emits OnStaleServed with nil hook).
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("fail")
	})
	// If we get here without panicking, the test passes.
}

// ---------------------------------------------------------------------------
// Success after stale serve refreshes cache and clears stale flag
// ---------------------------------------------------------------------------

func TestStaleCacheSuccessAfterStaleRefreshesCache(t *testing.T) {
	clk := newStaleClock()
	sc := NewStaleCache[string](time.Minute, clk, &Hooks{})

	// Seed cache.
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "old", nil
	})

	clk.advance(10 * time.Second)

	// Fail -> serve stale.
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("fail")
	})

	if !sc.ServingStale() {
		t.Fatal("ServingStale() = false, want true")
	}

	// Succeed again -> should refresh cache and clear stale flag.
	result, err := sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "new", nil
	})

	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "new" {
		t.Fatalf("Do() = %q, want %q", result, "new")
	}
	if sc.ServingStale() {
		t.Fatal("ServingStale() = true after refresh, want false")
	}
	if sc.StaleAge() != 0 {
		t.Fatalf("StaleAge() = %v, want 0 after refresh", sc.StaleAge())
	}
}

// ---------------------------------------------------------------------------
// TTL boundary: exactly at TTL should still be served
// ---------------------------------------------------------------------------

func TestStaleCacheTTLBoundaryExactlyAtTTL(t *testing.T) {
	clk := newStaleClock()
	ttl := time.Minute
	sc := NewStaleCache[string](ttl, clk, &Hooks{})

	// Seed cache.
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "boundary", nil
	})

	// Advance exactly to TTL.
	clk.advance(ttl)

	// Fail -> cache age equals TTL, should still serve.
	result, err := sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", errors.New("fail")
	})

	if err != nil {
		t.Fatalf("Do() error = %v, want nil (stale at exact TTL boundary)", err)
	}
	if result != "boundary" {
		t.Fatalf("Do() = %q, want %q", result, "boundary")
	}
	if !sc.ServingStale() {
		t.Fatal("ServingStale() = false, want true at TTL boundary")
	}
}

// ---------------------------------------------------------------------------
// TTL boundary: one nanosecond past TTL should expire
// ---------------------------------------------------------------------------

func TestStaleCacheTTLBoundaryJustPastTTL(t *testing.T) {
	clk := newStaleClock()
	ttl := time.Minute
	sc := NewStaleCache[string](ttl, clk, &Hooks{})

	// Seed cache.
	_, _ = sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "expired", nil
	})

	// Advance one nanosecond past TTL.
	clk.advance(ttl + time.Nanosecond)

	sentinel := errors.New("fail")
	result, err := sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "", sentinel
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("Do() error = %v, want %v", err, sentinel)
	}
	if result != "" {
		t.Fatalf("Do() = %q, want zero value", result)
	}
	if sc.ServingStale() {
		t.Fatal("ServingStale() = true past TTL, want false")
	}
}

// ---------------------------------------------------------------------------
// Benchmark: concurrent Do calls that hit cache
// ---------------------------------------------------------------------------

func BenchmarkStaleCacheHit(b *testing.B) {
	clk := newStaleClock()
	sc := NewStaleCache[string](time.Hour, clk, &Hooks{})

	// Seed the cache.
	sc.Do(context.Background(), func(_ context.Context) (string, error) {
		return "cached", nil
	})

	clk.advance(time.Second)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			sc.Do(context.Background(), func(_ context.Context) (string, error) {
				return "", errors.New("fail")
			})
		}
	})
}
