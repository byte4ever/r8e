package r8e

import (
	"math"
	"sync"
	"sync/atomic"
	"time"
)

// ---------------------------------------------------------------------------
// windowedPercentile — epoch-memoized percentile over a latency window
// ---------------------------------------------------------------------------.

// windowedPercentile reads a single percentile from a sliding-window
// [latencyWindow], memoizing the result for one window epoch so the per-call
// path skips the ring merge. It is the shared estimation core of the adaptive
// patterns that size a duration from observed latency — [adaptiveTimeout]
// (latency→timeout) and [adaptiveHedge] (latency→hedge delay) — each embedding
// it and adding only its own tunables and clamp.
//
// Reading a percentile merges the window's whole ring, an allocation too costly
// to repeat on every call, so the estimate is memoized per window epoch and
// refreshed at most once per bucket-sized slice. The refresh is elected under
// refreshMu so a burst at an epoch boundary merges once (a thundering-herd
// guard), and both the window and the refresh cadence are driven by the
// injected [Clock], so the estimate is deterministic under a fake clock in
// tests. Safe for concurrent use; embed it by pointer (it holds a mutex and
// atomics that must not be copied).
type windowedPercentile struct {
	clock  Clock
	window *latencyWindow
	// cachedValue, cachedSamples and cachedEpoch memoize the percentile estimate
	// for one window epoch so the per-call path skips the ring merge; refreshMu
	// elects a single refresher at each epoch boundary to avoid a thundering herd.
	cachedValue   atomic.Int64 // the percentile latency, in nanoseconds
	cachedSamples atomic.Int64
	cachedEpoch   atomic.Int64
	refreshMu     sync.Mutex
}

// newWindowedPercentile builds an estimator over a fresh latency window driven
// by clock. The cached epoch is seeded to a sentinel no real epoch equals so the
// first estimate always refreshes.
func newWindowedPercentile(clock Clock) *windowedPercentile {
	est := &windowedPercentile{
		clock:  clock,
		window: newLatencyWindow(clock),
	}
	est.cachedEpoch.Store(math.MinInt64)

	return est
}

// estimate returns the q-th percentile of recent latencies and the sample count
// behind it, memoized per window epoch. The percentile read merges the window's
// whole ring, so refreshing it on every call would dominate a fast policy's
// latency; instead it is refreshed at most once per bucket-sized epoch, elected
// under refreshMu so a burst at the epoch boundary merges once.
func (e *windowedPercentile) estimate(percentile float64) (latency time.Duration, samples int64) {
	epoch := e.window.epochOf(e.clock.Now())
	if e.cachedEpoch.Load() == epoch {
		return time.Duration(e.cachedValue.Load()), e.cachedSamples.Load()
	}

	e.refreshMu.Lock()
	defer e.refreshMu.Unlock()

	// Re-check under the lock: a racing caller may have refreshed this epoch.
	if e.cachedEpoch.Load() == epoch {
		return time.Duration(e.cachedValue.Load()), e.cachedSamples.Load()
	}

	latency, samples = e.window.quantileSnapshot(percentile)
	e.cachedValue.Store(int64(latency))
	e.cachedSamples.Store(samples)
	e.cachedEpoch.Store(epoch)

	return latency, samples
}

// observe folds one completed call's latency into the window. The caller decides
// what is representative (the adaptive patterns record successful calls only);
// observe itself records unconditionally.
func (e *windowedPercentile) observe(elapsed time.Duration) {
	e.window.observe(elapsed)
}

// invalidate drops the memoized estimate so the next estimate recomputes
// immediately rather than lagging until the current epoch rolls over — used when
// a reconfigure changes the driving percentile, which the epoch-keyed cache does
// not otherwise notice. The reset is taken under refreshMu so it cannot be
// clobbered by an in-flight refresher republishing the current epoch, which would
// silently restore the one-epoch staleness this invalidation removes.
func (e *windowedPercentile) invalidate() {
	e.refreshMu.Lock()
	e.cachedEpoch.Store(math.MinInt64)
	e.refreshMu.Unlock()
}
