package r8e

import (
	"context"
	"encoding/binary"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// relErr returns the relative error of got against want, used to assert the
// DDSketch percentile accuracy bound.
func relErr(got, want time.Duration) float64 {
	diff := float64(got - want)
	if diff < 0 {
		diff = -diff
	}

	return diff / float64(want)
}

// TestLatencyWindowEmpty proves a fresh window reports no percentiles rather
// than a misleading zero-latency reading.
func TestLatencyWindowEmpty(t *testing.T) {
	t.Parallel()

	w := newLatencyWindow(&stubClock{now: epochBase()})

	s := w.snapshot()

	assert.Zero(t, s.p50)
	assert.Zero(t, s.p95)
	assert.Zero(t, s.p99)
	assert.Zero(t, s.samples)
}

// TestLatencyWindowIndexBounds covers the floor, an interior value, and the
// ceiling of the DDSketch index mapping.
func TestLatencyWindowIndexBounds(t *testing.T) {
	t.Parallel()

	w := newLatencyWindow(&stubClock{now: epochBase()})

	assert.Equal(t, 0, w.index(0), "zero lands in the floor bucket")
	assert.Equal(t, 0, w.index(minLatencyNanos), "floor latency is bucket 0")
	assert.Equal(t, 0, w.index(minLatencyNanos-1), "below floor clamps to bucket 0")

	assert.Equal(t, w.size-1, w.index(maxLatencyNanos),
		"ceiling latency is the top bucket")
	assert.Equal(t, w.size-1, w.index(maxLatencyNanos*10),
		"beyond ceiling saturates the top bucket")

	// Interior values map to strictly increasing buckets as latency grows.
	prev := -1
	for _, d := range []time.Duration{
		time.Millisecond, 10 * time.Millisecond, 100 * time.Millisecond,
		time.Second, 10 * time.Second,
	} {
		idx := w.index(int64(d))
		assert.Greater(t, idx, prev, "index must increase with latency for %s", d)
		assert.Less(t, idx, w.size)
		prev = idx
	}
}

// TestLatencyWindowSinglePeak proves every percentile of a single repeated
// latency lands within the DDSketch relative-accuracy bound of that value.
func TestLatencyWindowSinglePeak(t *testing.T) {
	t.Parallel()

	w := newLatencyWindow(&stubClock{now: epochBase()})

	const want = 50 * time.Millisecond
	for range 1000 {
		w.observe(want)
	}

	s := w.snapshot()

	require.EqualValues(t, 1000, s.samples)
	assert.LessOrEqual(t, relErr(s.p50, want), latencyRelativeAccuracy)
	assert.LessOrEqual(t, relErr(s.p95, want), latencyRelativeAccuracy)
	assert.LessOrEqual(t, relErr(s.p99, want), latencyRelativeAccuracy)
}

// TestLatencyWindowSpread feeds a known ascending distribution and asserts the
// percentiles are ordered and each within the relative-accuracy bound of the
// exact rank value.
func TestLatencyWindowSpread(t *testing.T) {
	t.Parallel()

	w := newLatencyWindow(&stubClock{now: epochBase()})

	// 100 evenly spaced samples 1ms..100ms: the exact p50/p95/p99 by nearest-rank
	// are the 50th/95th/99th values = 50ms/95ms/99ms.
	for i := 1; i <= 100; i++ {
		w.observe(time.Duration(i) * time.Millisecond)
	}

	s := w.snapshot()

	require.EqualValues(t, 100, s.samples)
	assert.Less(t, s.p50, s.p95)
	assert.Less(t, s.p95, s.p99)
	assert.LessOrEqual(t, relErr(s.p50, 50*time.Millisecond), latencyRelativeAccuracy)
	assert.LessOrEqual(t, relErr(s.p95, 95*time.Millisecond), latencyRelativeAccuracy)
	assert.LessOrEqual(t, relErr(s.p99, 99*time.Millisecond), latencyRelativeAccuracy)
}

// TestLatencyWindowAgesOut proves samples drop out of the percentiles once the
// clock advances a full window past them.
func TestLatencyWindowAgesOut(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	w := newLatencyWindow(clk)

	for range 100 {
		w.observe(40 * time.Millisecond)
	}

	require.EqualValues(t, 100, w.snapshot().samples)

	// Advance past the whole window: every slice ages out.
	clk.now = clk.now.Add(defaultLatencyWindow + time.Second)

	s := w.snapshot()
	assert.Zero(t, s.samples, "all samples aged out of the window")
	assert.Zero(t, s.p50)
}

// TestLatencyWindowSlidingDecay proves only the samples still inside the window
// contribute: an old peak no longer skews the percentiles after the clock moves
// on while a recent peak does.
func TestLatencyWindowSlidingDecay(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	w := newLatencyWindow(clk)

	// Old, slow samples.
	for range 100 {
		w.observe(2 * time.Second)
	}

	// Move on by a full window, then add fast samples; the slow slice has just
	// aged past the trailing edge, so only the fast samples remain live.
	clk.now = clk.now.Add(defaultLatencyWindow)
	for range 100 {
		w.observe(5 * time.Millisecond)
	}

	s := w.snapshot()
	require.EqualValues(t, 100, s.samples, "only the recent slice is live")
	assert.LessOrEqual(t, relErr(s.p50, 5*time.Millisecond), latencyRelativeAccuracy)
}

// TestLatencyWindowNegativeDuration proves a backward clock step (negative
// elapsed) is absorbed into the floor bucket rather than panicking.
func TestLatencyWindowNegativeDuration(t *testing.T) {
	t.Parallel()

	w := newLatencyWindow(&stubClock{now: epochBase()})

	require.NotPanics(t, func() { w.observe(-5 * time.Millisecond) })

	s := w.snapshot()
	assert.EqualValues(t, 1, s.samples)
	// The clamped sample must land in the floor bucket, not merely "not panic".
	assert.Equal(t, w.valueAt(0), s.p50, "negative duration maps to the floor bucket")
}

// TestLatencyWindowInclusiveTrailingEdge proves a slice stamped exactly at the
// oldest live epoch still counts — the inclusive boundary of the window, one
// epoch younger than the aged-out slice TestLatencyWindowSlidingDecay drops.
func TestLatencyWindowInclusiveTrailingEdge(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase()}
	w := newLatencyWindow(clk)

	for range 40 {
		w.observe(25 * time.Millisecond)
	}

	// Advance by one epoch less than the full window: the original slice now sits
	// exactly on the oldest live epoch and must survive (epoch == oldest, kept).
	clk.now = clk.now.Add(defaultLatencyWindow - defaultLatencyWindow/latencyWindowBuckets)

	s := w.snapshot()
	assert.EqualValues(t, 40, s.samples, "slice on the oldest live epoch is kept")
	assert.LessOrEqual(t, relErr(s.p50, 25*time.Millisecond), latencyRelativeAccuracy)
}

// TestLatencyBucketTotalMatchesCounts pins the latencyBucket invariant
// total == sum(counts) across mixed observations, so an edit that updates one
// without the other is caught rather than silently skewing the quantile rank.
func TestLatencyBucketTotalMatchesCounts(t *testing.T) {
	t.Parallel()

	w := newLatencyWindow(&stubClock{now: epochBase()})

	for _, d := range []time.Duration{
		1 * time.Millisecond, 5 * time.Millisecond, 5 * time.Millisecond,
		200 * time.Millisecond, 3 * time.Second,
	} {
		w.observe(d)
	}

	for i := range w.buckets {
		bucket := &w.buckets[i]
		if bucket.counts == nil {
			continue
		}

		var sum int64
		for _, c := range bucket.counts {
			sum += c
		}

		assert.Equal(t, sum, bucket.total, "bucket %d: total must equal sum(counts)", i)
	}
}

// TestLatencyWindowClockRewindIgnoresFutureBucket proves a slice stamped with a
// future epoch (after the clock steps backward) is skipped by snapshot, mirroring
// the throttler window's guard.
func TestLatencyWindowClockRewindIgnoresFutureBucket(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase().Add(time.Hour)}
	w := newLatencyWindow(clk)

	for range 50 {
		w.observe(30 * time.Millisecond)
	}

	// Step the clock back before the slice we just wrote: it is now a future
	// bucket relative to "now" and must not be counted.
	clk.now = epochBase()

	assert.Zero(t, w.snapshot().samples,
		"future-stamped slice is ignored after a clock rewind")
}

// TestLatencyWindowConcurrentObserve runs many observers against one window to
// prove the mutex keeps the counts consistent under -race.
//
// The shared stubClock is documented as not concurrency-safe, but it is safe
// here: its now field is set once at construction and never written while the
// observers run, so every goroutine only reads it — no read/write race. A test
// that mutated clk.now mid-run would need a synchronised clock.
func TestLatencyWindowConcurrentObserve(t *testing.T) {
	t.Parallel()

	w := newLatencyWindow(&stubClock{now: epochBase()})

	const (
		goroutines = 16
		perG       = 500
	)

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)

		go func(g int) {
			defer wg.Done()

			d := time.Duration(g+1) * time.Millisecond
			for range perG {
				w.observe(d)
			}
		}(g)
	}

	wg.Wait()

	assert.EqualValues(t, goroutines*perG, w.snapshot().samples)
}

// TestLatencyWindowPreEpochClock proves a clock set before the Unix epoch
// (negative epoch) is handled by the non-negative ring-index fold rather than
// panicking on a negative slot.
func TestLatencyWindowPreEpochClock(t *testing.T) {
	t.Parallel()

	// An epoch not divisible by the ring size exercises the negative-modulo fold.
	w := newLatencyWindow(&stubClock{now: time.Unix(-100_003, 0)})

	require.NotPanics(t, func() {
		for range 30 {
			w.observe(20 * time.Millisecond)
		}
	})

	s := w.snapshot()
	assert.EqualValues(t, 30, s.samples)
	assert.LessOrEqual(t, relErr(s.p50, 20*time.Millisecond), latencyRelativeAccuracy)
}

// TestLatencyWindowQuantileBeyondCounts covers the defensive fallback: if a
// target rank exceeds the accumulated counts (an inconsistent total), quantile
// returns the top observed bucket rather than walking off the slice.
func TestLatencyWindowQuantileBeyondCounts(t *testing.T) {
	t.Parallel()

	w := newLatencyWindow(&stubClock{now: epochBase()})

	merged := make([]int64, w.size)
	merged[2] = 1

	// total (5) overstates the one count in merged, so the cumulative never
	// reaches the rank and the fallback returns the last bucket's value.
	got := w.quantile(merged, 5, 0.9)
	assert.Equal(t, w.valueAt(w.size-1), got)
}

// TestPolicyMetricsLatency proves Do() feeds the window and the percentiles
// surface through Metrics, measured on the injected clock.
func TestPolicyMetricsLatency(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: epochBase(), elapsed: 50 * time.Millisecond}
	p := NewPolicy[int]("lat", WithClock(clk), WithRegistry(NewRegistry()))

	for range 20 {
		_, err := p.Do(context.Background(), func(context.Context) (int, error) {
			return 1, nil
		})
		require.NoError(t, err)
	}

	m := p.Metrics()
	assert.EqualValues(t, 20, m.LatencySamples)
	assert.LessOrEqual(t, relErr(m.LatencyP50, 50*time.Millisecond), latencyRelativeAccuracy)
	assert.LessOrEqual(t, relErr(m.LatencyP99, 50*time.Millisecond), latencyRelativeAccuracy)
}

// TestPolicyMetricsLatencyEmpty proves a policy with no completed calls reports
// zero percentiles and zero samples.
func TestPolicyMetricsLatencyEmpty(t *testing.T) {
	t.Parallel()

	p := NewPolicy[int]("lat-empty", WithRegistry(NewRegistry()))

	m := p.Metrics()
	assert.Zero(t, m.LatencySamples)
	assert.Zero(t, m.LatencyP50)
	assert.Zero(t, m.LatencyP99)
}

// FuzzLatencyIndex asserts the pure DDSketch bucket mapping over the whole int64
// domain: index must always land in [0, size) — the property that keeps the
// counts[idx] write in observe panic-free — and valueAt of that bucket must be a
// finite, positive, in-range latency. Line coverage proves the clamps run; this
// proves no boundary input (MinInt64, MaxInt64, the floor/ceiling edges) slips
// through them.
func FuzzLatencyIndex(f *testing.F) {
	for _, n := range []int64{
		math.MinInt64, -1_000_000, -1, 0, 1, minLatencyNanos, minLatencyNanos + 1,
		1_000_000, int64(time.Second), maxLatencyNanos, maxLatencyNanos + 1, math.MaxInt64,
	} {
		f.Add(n)
	}

	w := newLatencyWindow(&stubClock{now: epochBase()})

	f.Fuzz(func(t *testing.T, nanos int64) {
		idx := w.index(nanos) // must not panic
		if idx < 0 || idx >= w.size {
			t.Fatalf("index(%d) = %d, outside [0, %d)", nanos, idx, w.size)
		}

		// valueAt over a valid bucket must be finite, positive, and no larger than
		// the ceiling representative — an overflow in math.Pow would breach this.
		v := w.valueAt(idx)
		if v <= 0 || v > w.valueAt(w.size-1) {
			t.Fatalf("valueAt(%d) = %v, outside (0, %v] (nanos=%d)",
				idx, v, w.valueAt(w.size-1), nanos)
		}
	})
}

// FuzzLatencyWindow drives a fuzzed stream of call durations (and clock
// advances) through a window and asserts the observable invariants hold for any
// input: observe never panics, the per-bucket total == sum(counts) coupling is
// preserved, the live sample count never exceeds what was observed, and a
// non-empty snapshot yields finite, monotone (p50 <= p95 <= p99), in-range
// percentiles. Mirrors FuzzAdaptiveRecompute's white-box sequence style.
func FuzzLatencyWindow(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 10})                         // 10ns
	f.Add([]byte{255, 255, 255, 255, 255, 255, 255, 255})          // -1ns → floor
	f.Add([]byte{127, 255, 255, 255, 255, 255, 255, 255})          // MaxInt64 → ceiling
	f.Add([]byte{0, 0, 0, 0, 0, 0, 0, 10, 0, 0, 0, 0, 0, 0, 0, 5}) // two samples

	f.Fuzz(func(t *testing.T, data []byte) {
		clk := &stubClock{now: epochBase()}
		w := newLatencyWindow(clk)

		var observed int64

		for len(data) >= 8 {
			raw := binary.BigEndian.Uint64(data[:8])
			data = data[8:]

			w.observe(time.Duration(int64(raw))) // must not panic
			observed++

			// Advance 0–3 bucket spans so rotation, reuse, and aging are exercised
			// deterministically; bounded, so the clock cannot overflow.
			clk.now = clk.now.Add(time.Duration(int64(raw&0x3) * w.bucketNanos))
		}

		// Structural invariant: every allocated bucket keeps total == sum(counts).
		for i := range w.buckets {
			bucket := &w.buckets[i]
			if bucket.counts == nil {
				continue
			}

			var sum int64
			for _, c := range bucket.counts {
				sum += c
			}

			if bucket.total != sum {
				t.Fatalf("bucket %d: total %d != sum(counts) %d", i, bucket.total, sum)
			}
		}

		s := w.snapshot()
		if s.samples < 0 || s.samples > observed {
			t.Fatalf("samples %d outside [0, %d]", s.samples, observed)
		}

		if s.samples == 0 {
			if s.p50 != 0 || s.p95 != 0 || s.p99 != 0 {
				t.Fatalf("empty window must report zero percentiles, got %v/%v/%v",
					s.p50, s.p95, s.p99)
			}

			return
		}

		if s.p50 > s.p95 || s.p95 > s.p99 {
			t.Fatalf("percentiles not monotone: p50=%v p95=%v p99=%v", s.p50, s.p95, s.p99)
		}

		if s.p50 < w.valueAt(0) || s.p99 > w.valueAt(w.size-1) {
			t.Fatalf("percentiles outside [%v, %v]: p50=%v p99=%v",
				w.valueAt(0), w.valueAt(w.size-1), s.p50, s.p99)
		}
	})
}
