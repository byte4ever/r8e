package r8e

import (
	"math"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// latencyWindow — Clock-driven sliding-window DDSketch of Do() latencies
// ---------------------------------------------------------------------------.

type (
	// latencyBucket is one time slice of the sliding latency window: a DDSketch
	// over the call durations observed during the epoch it is stamped with.
	// counts[i] is the number of samples that fell in DDSketch bucket i and total
	// is their sum. INVARIANT: total == sum(counts) — observe maintains both in
	// lock-step, and snapshot uses total as the quantile-rank denominator, so an
	// edit that touches one without the other silently collapses every percentile
	// to the ceiling bucket (pinned by TestLatencyBucketTotalMatchesCounts). A
	// bucket whose epoch has aged out of the window is reused — its counts cleared
	// in place — rather than reallocated; counts stays nil until the slot is first
	// written so a policy that observes only a few samples (e.g. the convenience
	// [Do]) allocates only the slices it touches.
	latencyBucket struct {
		counts []int64
		epoch  int64
		total  int64
	}

	// latencyWindow is a Clock-driven sliding-window histogram of end-to-end
	// [Policy.Do] latencies, used to expose recent p50/p95/p99 percentiles in
	// [PolicyMetrics].
	//
	// Storage is a DDSketch: bucket i covers the latency range (gamma^(i-1),
	// gamma^i] with gamma = (1+a)/(1-a), so a value's bucket is
	// ceil(log(value)/log(gamma)) and every reported percentile is within the
	// relative accuracy a (default 2%) of the true value — the property
	// fixed-boundary histograms lack. Indices are offset so bucket 0 is the floor
	// latency (latencies at or below it report as the floor) and clamped at the
	// ceiling (larger latencies report as the ceiling); both bounds are wide
	// enough (1us..2min) that only extreme outliers saturate.
	//
	// Recency comes from a ring of epoch-stamped sub-sketches, exactly as the
	// [Throttler] and circuit-breaker slow-call windows decay their counts: each
	// sub-sketch holds the samples of one bucket-sized time slice, and a slice
	// older than the window is skipped at query time (and reused in place on its
	// next write), so old latency ages out without explicit eviction. The window
	// is measured against the injected [Clock], so percentiles are deterministic
	// under a fake clock in tests.
	//
	// One mutex guards the ring: a sample's bucket increment and a percentile
	// query's merge across live slices must each see a consistent set of counts,
	// which independent atomics cannot provide once a slice rotates. It is safe
	// for concurrent use.
	latencyWindow struct {
		clock       Clock
		buckets     [latencyWindowBuckets]latencyBucket
		gamma       float64
		logGamma    float64
		bucketNanos int64
		floorRaw    int
		size        int
		mu          sync.Mutex
	}

	// latencySnapshot is the result of querying a [latencyWindow]: the percentile
	// latencies over the current window and the sample count they were computed
	// from. All four are zero when no call completed in the window.
	latencySnapshot struct {
		p50     time.Duration
		p95     time.Duration
		p99     time.Duration
		samples int64
	}
)

const (
	// latencyWindowBuckets is the number of time slices the sliding window is
	// divided into; a finer ring decays old samples more smoothly. Internal.
	latencyWindowBuckets = 10

	// defaultLatencyWindow is the span percentiles are computed over. It matches
	// the throttler's window so the two recency horizons agree; it is fixed
	// (zero-config) in this release.
	defaultLatencyWindow = 10 * time.Second

	// latencyRelativeAccuracy is the DDSketch relative-error bound a: every
	// reported percentile is within a*value of the true value. 0.02 (2%) needs
	// roughly 470 buckets to span the floor..ceiling range below.
	latencyRelativeAccuracy = 0.02

	// minLatencyNanos floors the indexable range at 1us: a Do() faster than this
	// reports as ~1us. Resilience latencies are rarely tuned below it.
	minLatencyNanos = 1000

	// maxLatencyNanos caps the indexable range at 2min: a Do() slower than this
	// saturates the top bucket and reports as ~2min. Such calls are extreme
	// outliers a percentile need not distinguish finely.
	maxLatencyNanos = int64(2 * time.Minute)
)

// newLatencyWindow builds a sliding-window DDSketch driven by clock. It derives
// the log-bucket geometry (gamma, the floor index offset, and the bucket count)
// from the fixed accuracy and range constants; the per-slice count slices are
// allocated lazily on first write.
func newLatencyWindow(clock Clock) *latencyWindow {
	gamma := (1 + latencyRelativeAccuracy) / (1 - latencyRelativeAccuracy)
	logGamma := math.Log(gamma)
	floorRaw := int(math.Ceil(math.Log(minLatencyNanos) / logGamma))
	ceilRaw := int(math.Ceil(math.Log(float64(maxLatencyNanos)) / logGamma))

	return &latencyWindow{
		clock:       clock,
		gamma:       gamma,
		logGamma:    logGamma,
		bucketNanos: int64(defaultLatencyWindow) / latencyWindowBuckets,
		floorRaw:    floorRaw,
		size:        ceilRaw - floorRaw + 1,
	}
}

// observe folds one completed [Policy.Do] latency into the window. It maps d to
// its DDSketch bucket and increments that bucket in the ring slice for the
// current epoch, resetting the slice first when it still holds an aged-out
// epoch's counts.
func (w *latencyWindow) observe(d time.Duration) {
	nanos := int64(d)
	if nanos < 0 {
		// A backward clock step can yield a negative elapsed; treat it as zero so
		// it lands in the floor bucket instead of corrupting the index.
		nanos = 0
	}

	idx := w.index(nanos)
	epoch := w.epochOf(w.clock.Now())

	w.mu.Lock()
	defer w.mu.Unlock()

	bucket := w.bucketFor(epoch)
	bucket.counts[idx]++
	bucket.total++
}

// bucketFor returns the ring slice that owns epoch, ready to be written. It
// claims the slot for epoch first: allocating the sketch on the slot's first
// ever use, or clearing a stale prior epoch's counts when the ring index is
// being reused for a newer slice. Mirrors the throttler's bucketFor. Must be
// called with w.mu held.
//
// This is exact-match slot claiming (epoch != current ⇒ reset), distinct from
// snapshot's range-based liveness test (isLive): a writer owns exactly one slot,
// whereas a reader sums every slot still inside the window.
func (w *latencyWindow) bucketFor(epoch int64) *latencyBucket {
	bucket := &w.buckets[w.slotFor(epoch)]

	switch {
	case bucket.counts == nil:
		// First write to this ring slot: allocate its sketch.
		bucket.counts = make([]int64, w.size) //nolint:makezero // index-addressed sketch
		bucket.total = 0
		bucket.epoch = epoch
	case bucket.epoch != epoch:
		// Slot is being reused for a new time slice: clear the stale counts.
		clear(bucket.counts)
		bucket.total = 0
		bucket.epoch = epoch
	default:
		// Same epoch as the last write: keep accumulating into the live slice.
	}

	return bucket
}

// snapshot merges every live (non-aged-out) ring slice and returns the p50, p95
// and p99 latencies over the window together with the sample count they were
// computed from. With no samples in the window it returns the zero snapshot — a
// zero percentile means "no recent calls", not a zero-latency call.
func (w *latencyWindow) snapshot() latencySnapshot {
	merged, total := w.merge()
	if total == 0 {
		return latencySnapshot{}
	}

	return latencySnapshot{
		p50:     w.quantile(merged, total, 0.50),
		p95:     w.quantile(merged, total, 0.95),
		p99:     w.quantile(merged, total, 0.99),
		samples: total,
	}
}

// quantileSnapshot returns the q-th percentile latency over the current window
// (q in (0, 1]) together with the sample count it was computed from. It is the
// single-percentile companion to snapshot, used by the adaptive timeout to read
// one arbitrary percentile (its driving percentile) without computing the fixed
// p50/p95/p99 triple. With no samples in the window it returns (0, 0) — a zero
// latency means "no recent calls", not a zero-latency call.
func (w *latencyWindow) quantileSnapshot(q float64) (latency time.Duration, samples int64) {
	merged, total := w.merge()
	if total == 0 {
		return 0, 0
	}

	return w.quantile(merged, total, q), total
}

// merge sums every live ring slice into one DDSketch and returns it with the
// total sample count. The window edges are resolved from the clock once, then
// each slice is folded in under the lock; the per-slice quantile derivation
// happens after the caller unlocks (snapshot computes on the private merged
// buffer). Returns an all-zero merged buffer and 0 when the window is empty.
func (w *latencyWindow) merge() (merged []int64, total int64) {
	current := w.epochOf(w.clock.Now())
	oldest := current - latencyWindowBuckets + 1

	merged = make([]int64, w.size) //nolint:makezero // index-addressed merge buffer

	w.mu.Lock()

	for i := range w.buckets {
		bucket := &w.buckets[i]
		if !isLive(bucket, oldest, current) {
			continue
		}

		for j, c := range bucket.counts {
			merged[j] += c
		}

		total += bucket.total
	}

	w.mu.Unlock()

	return merged, total
}

// isLive reports whether bucket contributes to the current window: it must hold
// samples (allocated and non-empty) and be stamped within [oldest, current] —
// the upper bound excludes a future slice left by a backward clock step. Must be
// called with the window mutex held.
func isLive(bucket *latencyBucket, oldest, current int64) bool {
	return bucket.counts != nil && bucket.total > 0 &&
		bucket.epoch >= oldest && bucket.epoch <= current
}

// quantile returns the q-th percentile (q in (0, 1]) of the merged DDSketch
// counts holding total samples: the smallest bucket whose cumulative count
// reaches rank q*total, mapped back to a representative latency by valueAt.
func (w *latencyWindow) quantile(merged []int64, total int64, q float64) time.Duration {
	target := q * float64(total)

	var cum int64
	for i, c := range merged {
		cum += c
		if float64(cum) >= target {
			return w.valueAt(i)
		}
	}

	return w.valueAt(w.size - 1)
}

// index maps a latency in nanoseconds to its DDSketch bucket. The raw DDSketch
// index is ceil(log(nanos)/log(gamma)) — bucket i covers (gamma^(i-1), gamma^i];
// it is offset by floorRaw so the floor latency is array bucket 0, and clamped at
// the top so a value beyond the ceiling saturates rather than overflowing.
//
// The offset index cannot go negative: floorRaw is itself ceil(log(floor)/log
// gamma) computed from minLatencyNanos in newLatencyWindow (one source, not two
// coupled constants), so for any nanos > minLatencyNanos the raw index is >=
// floorRaw and idx >= 0. Lowering minLatencyNanos recomputes floorRaw in lock
// step, so the guarantee holds by construction — no runtime clamp is needed below.
func (w *latencyWindow) index(nanos int64) int {
	if nanos <= minLatencyNanos {
		return 0
	}

	idx := int(math.Ceil(math.Log(float64(nanos))/w.logGamma)) - w.floorRaw
	if idx >= w.size {
		return w.size - 1
	}

	return idx
}

// valueAt returns the representative latency for bucket idx. index and valueAt
// are deliberately NOT inverses: index rounds UP to a bucket, while valueAt
// returns the DDSketch point estimator 2*gamma^raw/(gamma+1) — the value sitting
// where the worst-case relative error over the bucket's (gamma^(raw-1), gamma^raw]
// range is minimised at exactly (gamma-1)/(gamma+1) = the configured accuracy.
// Using gamma^raw (the bucket's upper edge) instead would double that error.
func (w *latencyWindow) valueAt(idx int) time.Duration {
	raw := float64(w.floorRaw + idx)

	return time.Duration(2 * math.Pow(w.gamma, raw) / (w.gamma + 1))
}

// epochOf maps a timestamp to the monotonic index of the bucket-sized time slice
// it falls in — the counter the sliding window is expressed in.
func (w *latencyWindow) epochOf(now time.Time) int64 {
	return now.UnixNano() / w.bucketNanos
}

// slotFor returns the ring index holding epoch's slice. Two epochs a full window
// apart share a slot; the epoch stamp on the bucket distinguishes them so a
// reused slot is reset before its counts are read or written.
func (*latencyWindow) slotFor(epoch int64) int {
	idx := epoch % latencyWindowBuckets
	if idx < 0 {
		idx += latencyWindowBuckets
	}

	return int(idx)
}
