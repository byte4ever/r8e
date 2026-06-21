package r8e_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Acquire under limit succeeds
// ---------------------------------------------------------------------------

func TestBulkheadAcquireUnderLimit(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(3, r8e.RealClock{}, &r8e.Hooks{})

	require.NoError(t, bh.Acquire(t.Context()))
	require.NoError(t, bh.Acquire(t.Context()))
	require.NoError(t, bh.Acquire(t.Context()))
}

// ---------------------------------------------------------------------------
// Acquire at limit returns ErrBulkheadFull
// ---------------------------------------------------------------------------

func TestBulkheadAcquireAtLimitReturnsErrBulkheadFull(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(2, r8e.RealClock{}, &r8e.Hooks{})

	// Fill up both slots.
	require.NoError(t, bh.Acquire(t.Context()))
	require.NoError(t, bh.Acquire(t.Context()))

	// Third acquire should fail.
	require.ErrorIs(t, bh.Acquire(t.Context()), r8e.ErrBulkheadFull)
}

// ---------------------------------------------------------------------------
// Release frees a slot (can acquire again)
// ---------------------------------------------------------------------------

func TestBulkheadReleaseFreesSlot(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(1, r8e.RealClock{}, &r8e.Hooks{})

	require.NoError(t, bh.Acquire(t.Context()))

	// At capacity — should fail.
	require.ErrorIs(t, bh.Acquire(t.Context()), r8e.ErrBulkheadFull)

	// Release and try again.
	bh.Release()

	require.NoError(t, bh.Acquire(t.Context()))
}

// ---------------------------------------------------------------------------
// Full() returns correct state
// ---------------------------------------------------------------------------

func TestBulkheadFullReturnsCorrectState(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(2, r8e.RealClock{}, &r8e.Hooks{})

	require.False(t, bh.Full())

	bh.Acquire(t.Context())
	require.False(t, bh.Full())

	bh.Acquire(t.Context())
	require.True(t, bh.Full())

	bh.Release()
	require.False(t, bh.Full())
}

// ---------------------------------------------------------------------------
// Concurrent acquire/release (100 goroutines)
// ---------------------------------------------------------------------------

func TestBulkheadConcurrentAccess(t *testing.T) {
	t.Parallel()

	const maxConcurrent = 10
	const goroutines = 100

	bh := r8e.NewBulkhead(maxConcurrent, r8e.RealClock{}, &r8e.Hooks{})

	var wg sync.WaitGroup
	wg.Add(goroutines)

	var fullCount atomic.Int64

	for range goroutines {
		go func() {
			defer wg.Done()

			if err := bh.Acquire(t.Context()); err != nil {
				fullCount.Add(1)
				return
			}
			// Simulate work — no sleep needed, just release.
			_ = bh.Full()
			bh.Release()
		}()
	}

	wg.Wait()

	// After all goroutines finish, bulkhead should be empty.
	require.False(t, bh.Full())
}

// ---------------------------------------------------------------------------
// Hook emissions: Acquired, Full, Released
// ---------------------------------------------------------------------------

func TestBulkheadHookEmissions(t *testing.T) {
	t.Parallel()

	var acquiredCount, fullCount, releasedCount atomic.Int64
	hooks := &r8e.Hooks{
		OnBulkheadAcquired: func() { acquiredCount.Add(1) },
		OnBulkheadFull:     func() { fullCount.Add(1) },
		OnBulkheadReleased: func() { releasedCount.Add(1) },
	}

	bh := r8e.NewBulkhead(1, r8e.RealClock{}, hooks)

	// Acquire — should fire Acquired hook.
	bh.Acquire(t.Context())
	require.Equal(t, int64(1), acquiredCount.Load())

	// Acquire at capacity — should fire Full hook.
	bh.Acquire(t.Context())
	require.Equal(t, int64(1), fullCount.Load())

	// Release — should fire Released hook.
	bh.Release()
	require.Equal(t, int64(1), releasedCount.Load())
}

// ---------------------------------------------------------------------------
// Multiple sequential acquire/release cycles
// ---------------------------------------------------------------------------

func TestBulkheadMultipleSequentialCycles(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(1, r8e.RealClock{}, &r8e.Hooks{})

	for i := range 10 {
		require.NoErrorf(t, bh.Acquire(t.Context()), "cycle %d", i)
		require.Truef(t, bh.Full(), "cycle %d", i)
		require.ErrorIsf(t, bh.Acquire(t.Context()), r8e.ErrBulkheadFull, "cycle %d", i)
		bh.Release()
		require.Falsef(t, bh.Full(), "cycle %d", i)
	}
}

// ---------------------------------------------------------------------------
// Nil hooks don't panic
// ---------------------------------------------------------------------------

func TestBulkheadNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(1, r8e.RealClock{}, nil) // nil *Hooks must be a no-op

	bh.Acquire(t.Context())
	bh.Release()
	bh.Full()
}

// ---------------------------------------------------------------------------
// Single slot bulkhead (edge case)
// ---------------------------------------------------------------------------

func TestBulkheadSingleSlot(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(1, r8e.RealClock{}, &r8e.Hooks{})

	require.NoError(t, bh.Acquire(t.Context()))
	require.True(t, bh.Full())

	require.ErrorIs(t, bh.Acquire(t.Context()), r8e.ErrBulkheadFull)

	bh.Release()
	require.False(t, bh.Full())
}

// ---------------------------------------------------------------------------
// Bounded FIFO wait (C4) — controllable clock so the max-wait timer fires only
// on demand, making grant-vs-timeout deterministic.
// ---------------------------------------------------------------------------

type manualClock struct {
	mu     sync.Mutex
	timers []*manualTimer
}

func (c *manualClock) Now() time.Time              { return time.Unix(0, 0) }
func (*manualClock) Since(time.Time) time.Duration { return 0 }

//nolint:ireturn // satisfies the r8e.Timer interface by design
func (c *manualClock) NewTimer(time.Duration) r8e.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()

	tm := &manualTimer{ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, tm)

	return tm
}

// fireAll fires every timer handed out so far, simulating max-wait elapsing.
func (c *manualClock) fireAll() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, tm := range c.timers {
		tm.fire()
	}
}

type manualTimer struct {
	mu      sync.Mutex
	ch      chan time.Time
	stopped bool
}

func (t *manualTimer) C() <-chan time.Time { return t.ch }

func (t *manualTimer) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	was := !t.stopped
	t.stopped = true

	return was
}

func (*manualTimer) Reset(time.Duration) bool { return false }

func (t *manualTimer) fire() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.stopped {
		return
	}

	select {
	case t.ch <- time.Unix(0, 0):
	default:
	}
}

// startWaiter launches bh.Acquire(ctx) in a goroutine and blocks until the call
// has entered the wait queue, returning a channel carrying its eventual result.
func startWaiter(
	ctx context.Context,
	t *testing.T,
	bh *r8e.Bulkhead,
	wantQueued int64,
) <-chan error {
	t.Helper()

	res := make(chan error, 1)
	go func() { res <- bh.Acquire(ctx) }()

	require.Eventually(t, func() bool { return bh.Queued() == wantQueued },
		time.Second, time.Millisecond, "waiter did not enqueue")

	return res
}

// TestBulkheadWaitGrantedOnRelease: a queued caller is handed the slot a holder
// releases (FIFO handoff), returns nil, and fires OnBulkheadAcquired on the
// wait-then-grant path.
func TestBulkheadWaitGrantedOnRelease(t *testing.T) {
	t.Parallel()

	var acquired atomic.Int64

	mc := &manualClock{}
	bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{
		OnBulkheadAcquired: func() { acquired.Add(1) },
	}, r8e.BulkheadMaxWait(time.Hour))

	require.NoError(t, bh.Acquire(t.Context())) // fill the single slot
	require.Equal(t, int64(1), acquired.Load())

	res := startWaiter(t.Context(), t, bh, 1)
	require.Equal(t, int64(1), bh.Queued())

	bh.Release() // hand the slot to the waiter

	require.NoError(t, <-res)
	require.Zero(t, bh.Queued())
	require.True(t, bh.Full())                  // the waiter now holds the slot
	require.Equal(t, int64(2), acquired.Load()) // grant-after-wait fired the hook
}

// TestBulkheadWaitFIFOOrder: with two queued callers, the first to enqueue is the
// first served when a slot frees (FIFO, not LIFO).
func TestBulkheadWaitFIFOOrder(t *testing.T) {
	t.Parallel()

	mc := &manualClock{}
	bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{},
		r8e.BulkheadMaxWait(time.Hour), r8e.BulkheadQueueDepth(2))

	require.NoError(t, bh.Acquire(t.Context())) // hold the only slot

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resA := startWaiter(ctx, t, bh, 1) // A enqueues first → head
	resB := startWaiter(ctx, t, bh, 2) // B enqueues second

	bh.Release() // one freed slot must go to the head, A

	require.NoError(t, <-resA)
	require.Equal(t, int64(1), bh.Queued()) // B is still waiting

	select {
	case <-resB:
		t.Fatal("B was served before A — FIFO order violated")
	default:
	}

	cancel() // drain B
	require.ErrorIs(t, <-resB, context.Canceled)
}

// TestBulkheadWaitTimeout: a queued caller that waits the full max-wait gives up
// with ErrBulkheadTimeout and fires OnBulkheadTimeout.
func TestBulkheadWaitTimeout(t *testing.T) {
	t.Parallel()

	var timeouts atomic.Int64

	mc := &manualClock{}
	bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{
		OnBulkheadTimeout: func() { timeouts.Add(1) },
	}, r8e.BulkheadMaxWait(time.Hour))

	require.NoError(t, bh.Acquire(t.Context()))

	res := startWaiter(t.Context(), t, bh, 1)

	mc.fireAll() // max-wait elapses

	require.ErrorIs(t, <-res, r8e.ErrBulkheadTimeout)
	require.Equal(t, int64(1), timeouts.Load())
	require.Zero(t, bh.Queued())
}

// TestBulkheadWaitQueueFull: when the bounded queue is at depth, a full bulkhead
// rejects immediately with ErrBulkheadFull instead of queueing.
func TestBulkheadWaitQueueFull(t *testing.T) {
	t.Parallel()

	mc := &manualClock{}
	bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{},
		r8e.BulkheadMaxWait(time.Hour),
		r8e.BulkheadQueueDepth(1))

	require.NoError(t, bh.Acquire(t.Context())) // slot taken

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	res := startWaiter(ctx, t, bh, 1) // fills the depth-1 queue

	// Queue is full: the next caller is rejected immediately, without waiting.
	require.ErrorIs(t, bh.Acquire(t.Context()), r8e.ErrBulkheadFull)

	cancel() // drain the queued waiter
	require.ErrorIs(t, <-res, context.Canceled)
}

// TestBulkheadWaitContextCancelled: cancelling ctx while queued returns the
// context error, not a bulkhead error.
func TestBulkheadWaitContextCancelled(t *testing.T) {
	t.Parallel()

	mc := &manualClock{}
	bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{},
		r8e.BulkheadMaxWait(time.Hour))

	require.NoError(t, bh.Acquire(t.Context()))

	ctx, cancel := context.WithCancel(context.Background())
	res := startWaiter(ctx, t, bh, 1)

	cancel()

	require.ErrorIs(t, <-res, context.Canceled)
	require.Zero(t, bh.Queued())
}

// TestBulkheadReconfigureGrowWakesWaiters: raising the concurrency limit grants
// slots to queued callers without waiting for a Release.
func TestBulkheadReconfigureGrowWakesWaiters(t *testing.T) {
	t.Parallel()

	mc := &manualClock{}
	bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{},
		r8e.BulkheadMaxWait(time.Hour))

	require.NoError(t, bh.Acquire(t.Context()))

	res := startWaiter(t.Context(), t, bh, 1)

	bh.Reconfigure(2) // capacity opens up

	require.NoError(t, <-res)
	require.Equal(t, int64(2), bh.InUse())
}

// TestBulkheadQueueDepthDefaultsToCapacity: BulkheadMaxWait alone queues up to
// the concurrency limit before rejecting.
func TestBulkheadQueueDepthDefaultsToCapacity(t *testing.T) {
	t.Parallel()

	mc := &manualClock{}
	bh := r8e.NewBulkhead(2, mc, &r8e.Hooks{},
		r8e.BulkheadMaxWait(time.Hour))

	require.NoError(t, bh.Acquire(t.Context()))
	require.NoError(t, bh.Acquire(t.Context())) // both slots taken

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Default queue depth equals capacity (2): two callers may wait.
	r1 := startWaiter(ctx, t, bh, 1)
	r2 := startWaiter(ctx, t, bh, 2)

	// A third would exceed the default depth and be rejected immediately.
	require.ErrorIs(t, bh.Acquire(t.Context()), r8e.ErrBulkheadFull)

	cancel()
	assert.ErrorIs(t, <-r1, context.Canceled)
	assert.ErrorIs(t, <-r2, context.Canceled)
}

// TestBulkheadWaitQueuedHook: entering the wait queue fires OnBulkheadQueued.
func TestBulkheadWaitQueuedHook(t *testing.T) {
	t.Parallel()

	var queued atomic.Int64

	mc := &manualClock{}
	bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{
		OnBulkheadQueued: func() { queued.Add(1) },
	}, r8e.BulkheadMaxWait(time.Hour))

	require.NoError(t, bh.Acquire(t.Context()))

	res := startWaiter(t.Context(), t, bh, 1)
	require.Equal(t, int64(1), queued.Load())

	bh.Release()
	require.NoError(t, <-res)
}

// TestPolicyBulkheadWaitMiddleware: the policy middleware wires the bounded wait
// end to end — a queued call times out with ErrBulkheadTimeout and the metric
// counters reflect it.
func TestPolicyBulkheadWaitMiddleware(t *testing.T) {
	t.Parallel()

	var userTimeout atomic.Int64

	mc := &manualClock{}
	p := r8e.NewPolicy[string]("bulkhead-wait",
		r8e.WithClock(mc),
		r8e.WithHooks(&r8e.Hooks{
			OnBulkheadTimeout: func() { userTimeout.Add(1) },
		}),
		r8e.WithBulkhead(1,
			r8e.BulkheadMaxWait(time.Hour),
			r8e.BulkheadQueueDepth(1)),
	)

	hold := make(chan struct{})
	holding := make(chan struct{})

	go func() {
		_, _ = p.Do(t.Context(), func(_ context.Context) (string, error) {
			close(holding)
			<-hold // pin the only slot

			return "ok", nil
		})
	}()
	<-holding

	res := make(chan error, 1)
	go func() {
		_, err := p.Do(t.Context(), func(_ context.Context) (string, error) {
			return "ok", nil
		})
		res <- err
	}()

	require.Eventually(t, func() bool { return p.Metrics().BulkheadQueued == 1 },
		time.Second, time.Millisecond)

	mc.fireAll() // the queued call's max-wait elapses

	require.ErrorIs(t, <-res, r8e.ErrBulkheadTimeout)
	assert.Equal(t, int64(1), p.Metrics().BulkheadTimeouts)
	assert.Equal(t, int64(1), userTimeout.Load())

	close(hold) // let the holder finish
}

// TestBulkheadReconfigureWaitParams: Reconfigure applies new max-wait and queue
// depth from options.
func TestBulkheadReconfigureWaitParams(t *testing.T) {
	t.Parallel()

	mc := &manualClock{}
	bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{},
		r8e.BulkheadMaxWait(time.Hour))

	require.NoError(t, bh.Acquire(t.Context())) // hold the slot

	// Keep one slot but widen the queue to depth 2.
	bh.Reconfigure(1, r8e.BulkheadMaxWait(time.Hour), r8e.BulkheadQueueDepth(2))
	require.Equal(t, int64(1), bh.Cap())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r1 := startWaiter(ctx, t, bh, 1)
	r2 := startWaiter(ctx, t, bh, 2)

	// Depth 2 is full now; a third caller is rejected immediately.
	require.ErrorIs(t, bh.Acquire(t.Context()), r8e.ErrBulkheadFull)

	cancel()
	assert.ErrorIs(t, <-r1, context.Canceled)
	assert.ErrorIs(t, <-r2, context.Canceled)
}

// TestBulkheadGrantRacesTimeout stresses the race between a Release handing off a
// slot and the waiter's max-wait firing. Whichever wins, slot accounting stays
// consistent: a grant yields nil with the slot held, a timeout yields
// ErrBulkheadTimeout with the slot returned — never a leak.
func TestBulkheadGrantRacesTimeout(t *testing.T) {
	t.Parallel()

	for range 300 {
		mc := &manualClock{}
		bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{},
			r8e.BulkheadMaxWait(time.Hour))
		require.NoError(t, bh.Acquire(t.Context()))

		res := make(chan error, 1)
		go func() { res <- bh.Acquire(t.Context()) }()
		require.Eventually(t, func() bool { return bh.Queued() == 1 },
			time.Second, time.Microsecond)

		var wg sync.WaitGroup

		wg.Add(2)

		go func() { defer wg.Done(); bh.Release() }()
		go func() { defer wg.Done(); mc.fireAll() }()
		wg.Wait()

		if err := <-res; err == nil {
			require.Equal(t, int64(1), bh.InUse()) // waiter holds the slot
		} else {
			require.ErrorIs(t, err, r8e.ErrBulkheadTimeout)
			require.Equal(t, int64(0), bh.InUse()) // slot returned
		}

		require.Zero(t, bh.Queued()) // the waiter is no longer queued either way
	}
}

// TestBulkheadGrantRacesCancel stresses the race between a Release handoff and the
// waiter's context cancellation, asserting the same no-leak invariant.
func TestBulkheadGrantRacesCancel(t *testing.T) {
	t.Parallel()

	for range 300 {
		mc := &manualClock{}
		bh := r8e.NewBulkhead(1, mc, &r8e.Hooks{},
			r8e.BulkheadMaxWait(time.Hour))
		require.NoError(t, bh.Acquire(t.Context()))

		ctx, cancel := context.WithCancel(context.Background())

		res := make(chan error, 1)
		go func() { res <- bh.Acquire(ctx) }()
		require.Eventually(t, func() bool { return bh.Queued() == 1 },
			time.Second, time.Microsecond)

		var wg sync.WaitGroup

		wg.Add(2)

		go func() { defer wg.Done(); bh.Release() }()
		go func() { defer wg.Done(); cancel() }()
		wg.Wait()

		if err := <-res; err == nil {
			require.Equal(t, int64(1), bh.InUse()) // waiter holds the slot
		} else {
			require.ErrorIs(t, err, context.Canceled)
			require.Equal(t, int64(0), bh.InUse()) // slot returned
		}

		require.Zero(t, bh.Queued()) // the waiter is no longer queued either way
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkBulkheadAcquireRelease(b *testing.B) {
	bh := r8e.NewBulkhead(1000, r8e.RealClock{}, &r8e.Hooks{})

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := bh.Acquire(b.Context()); err == nil {
				bh.Release()
			}
		}
	})
}
