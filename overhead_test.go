package r8e

import (
	"context"
	"sync"
	"testing"
	"time"
)

// benchCache is a minimal in-package map cache for the cache-hit overhead
// benchmark (the external test package's testCache is not visible here).
type benchCache[K comparable, V any] struct {
	m  map[K]V
	mu sync.RWMutex
}

func newBenchCache[K comparable, V any]() *benchCache[K, V] {
	return &benchCache[K, V]{m: make(map[K]V)}
}

func (c *benchCache[K, V]) Get(key K) (V, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	v, ok := c.m[key]

	return v, ok
}

func (c *benchCache[K, V]) Set(key K, value V, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.m[key] = value
}

func (c *benchCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.m, key)
}

// BenchmarkOverhead measures the per-call CPU cost the library adds on the
// HAPPY PATH — fn succeeds, nothing trips. Each sub-benchmark isolates one
// component so its marginal cost is (component - baseline). Read it with
// -benchmem for ns/op + allocs/op:
//
//	go test -run '^$' -bench '^BenchmarkOverhead$' -benchmem ./
//
// All policies use an empty name so none auto-registers (registry cost excluded)
// and the always-on latency histogram is present in every Do path (see the
// empty-policy sub-bench for its share, and BenchmarkLatencyObserve for its
// standalone cost).
func BenchmarkOverhead(b *testing.B) {
	ctx := context.Background()
	okFn := func(_ context.Context) (string, error) { return "ok", nil }

	// baseline: the bare function call, the floor every component is measured
	// against.
	b.Run("baseline-raw-fn", func(b *testing.B) {
		b.ReportAllocs()

		for b.Loop() {
			_, _ = okFn(ctx)
		}
	})

	// empty-policy: NewPolicy with no patterns — the irreducible Do overhead
	// (chain application + the always-on latency histogram + two clock reads).
	b.Run("empty-policy", func(b *testing.B) {
		runDo(b, ctx, okFn)
	})

	b.Run("fallback", func(b *testing.B) {
		runDo(b, ctx, okFn, WithFallback("fb"))
	})

	b.Run("timeout", func(b *testing.B) {
		runDo(b, ctx, okFn, WithTimeout(time.Hour))
	})

	b.Run("circuit-breaker", func(b *testing.B) {
		runDo(b, ctx, okFn, WithCircuitBreaker())
	})

	b.Run("rate-limit", func(b *testing.B) {
		// A huge rate so no call is ever limited — pure admission overhead.
		runDo(b, ctx, okFn, WithRateLimit(1e12))
	})

	b.Run("bulkhead", func(b *testing.B) {
		runDo(b, ctx, okFn, WithBulkhead(1<<20))
	})

	b.Run("adaptive-concurrency", func(b *testing.B) {
		runDo(b, ctx, okFn, WithAdaptiveConcurrency())
	})

	b.Run("throttle", func(b *testing.B) {
		runDo(b, ctx, okFn, WithAdaptiveThrottle())
	})

	b.Run("retry", func(b *testing.B) {
		// Succeeds first try: retry machinery set up but never re-invoked.
		runDo(b, ctx, okFn, WithRetry(3, ConstantBackoff(time.Millisecond)))
	})

	b.Run("retry+time-budget", func(b *testing.B) {
		// Time budget needs a consumer (retry/hedge); measured over retry.
		runDo(b, ctx, okFn,
			WithRetry(3, ConstantBackoff(time.Millisecond)),
			WithTimeBudget(time.Hour))
	})

	b.Run("hedge", func(b *testing.B) {
		// Long delay so the hedge never fires; primary wins instantly.
		runDo(b, ctx, okFn, WithHedge(time.Hour))
	})

	b.Run("coalesce+timeout", func(b *testing.B) {
		// Coalesce requires a timeout to bound the detached shared call; its
		// marginal cost is this minus the timeout sub-bench.
		runDo(b, ctx, okFn,
			WithTimeout(time.Hour),
			WithCoalesce(func(context.Context) string { return "k" }))
	})

	b.Run("recover", func(b *testing.B) {
		runDo(b, ctx, okFn, WithRecover())
	})

	b.Run("cache-hit", func(b *testing.B) {
		cache := newBenchCache[string, CacheEntry[string]]()
		p := NewPolicy[string]("",
			WithCache[string](cache, func(context.Context) string { return "k" }, time.Hour))

		// Warm the cache so every measured call is a fresh hit (short-circuits
		// the whole chain).
		_, _ = p.Do(ctx, okFn)

		b.ReportAllocs()
		b.ResetTimer()

		for b.Loop() {
			_, _ = p.Do(ctx, okFn)
		}
	})

	// kitchen-sink: a realistic production stack, to show the stacked worst-case
	// per-call overhead when many patterns are layered.
	b.Run("kitchen-sink", func(b *testing.B) {
		runDo(b, ctx, okFn,
			WithTimeout(time.Hour),
			WithCircuitBreaker(),
			WithRateLimit(1e12),
			WithBulkhead(1<<20),
			WithRetry(3, ConstantBackoff(time.Millisecond)),
			WithHedge(time.Hour),
			WithFallback("fb"),
		)
	})
}

// runDo builds a nameless policy with opts and benchmarks Do on the happy path.
func runDo(
	b *testing.B,
	ctx context.Context,
	fn func(context.Context) (string, error),
	opts ...Option,
) {
	b.Helper()

	p := NewPolicy[string]("", opts...)

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_, _ = p.Do(ctx, fn)
	}
}

// BenchmarkLatencyObserve isolates the always-on latency histogram's per-call
// cost: one observe (the work Do does after every call). It is the dominant
// share of the empty-policy overhead.
func BenchmarkLatencyObserve(b *testing.B) {
	w := newLatencyWindow(&stubClock{now: epochBase()})

	b.ReportAllocs()

	for b.Loop() {
		w.observe(42 * time.Millisecond)
	}
}

// BenchmarkLatencyObserveParallel measures observe under contention — many
// goroutines hammering ONE window's mutex — to quantify the always-on
// histogram's worst-case scaling cost when a single hot policy is called
// concurrently. Run with -cpu=1,2,4,8 to see the contention curve.
func BenchmarkLatencyObserveParallel(b *testing.B) {
	w := newLatencyWindow(&stubClock{now: epochBase()})

	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			w.observe(42 * time.Millisecond)
		}
	})
}

// BenchmarkLatencySnapshot measures the cost of a percentile read (what
// Metrics() pays): merge the live ring then derive p50/p95/p99.
func BenchmarkLatencySnapshot(b *testing.B) {
	w := newLatencyWindow(&stubClock{now: epochBase()})
	for range 1000 {
		w.observe(42 * time.Millisecond)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for b.Loop() {
		_ = w.snapshot()
	}
}
