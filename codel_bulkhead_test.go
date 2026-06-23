package r8e_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	codelTarget   = 5 * time.Millisecond
	codelInterval = 100 * time.Millisecond
)

// codelClock is an advanceable fake clock: unlike manualClock (frozen at epoch),
// its Now() returns a settable instant so dwell times and the CoDel interval can
// be driven deterministically. Set the absolute offset from epoch with set().
type codelClock struct {
	mu  sync.Mutex
	now time.Time
}

func newCodelClock() *codelClock {
	return &codelClock{now: time.Unix(0, 0)}
}

func (c *codelClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *codelClock) Since(t time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now.Sub(t)
}

func (c *codelClock) set(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = time.Unix(0, 0).Add(d)
}

//nolint:ireturn // satisfies the r8e.Timer interface by design
func (c *codelClock) NewTimer(time.Duration) r8e.Timer {
	return neverTimer{}
}

// neverTimer is a Timer that never fires — used when a CoDel-only queue (no
// max-wait) installs no timer, so the timer is irrelevant.
type neverTimer struct{}

func (neverTimer) C() <-chan time.Time      { return nil }
func (neverTimer) Stop() bool               { return true }
func (neverTimer) Reset(time.Duration) bool { return false }

// TestBulkheadCoDelEnablesQueueWithoutMaxWait: BulkheadCoDel alone enables the
// bounded wait, so a full bulkhead queues instead of rejecting immediately, and a
// healthy queue (no standing delay) hands the freed slot over FIFO.
func TestBulkheadCoDelEnablesQueueWithoutMaxWait(t *testing.T) {
	t.Parallel()

	cc := newCodelClock()
	bh := r8e.NewBulkhead(1, cc, &r8e.Hooks{},
		r8e.BulkheadCoDel(codelTarget, codelInterval))

	require.NoError(t, bh.Acquire(t.Context())) // hold the only slot

	// Full + CoDel enabled → the caller queues rather than ErrBulkheadFull.
	res := startWaiter(t.Context(), t, bh, 1)
	require.Equal(t, int64(1), bh.Queued())

	bh.Release() // standing delay is 0 → healthy → FIFO grant

	require.NoError(t, <-res)
	require.Zero(t, bh.Queued())
	require.False(t, bh.Overloaded())
}

// TestBulkheadCoDelHealthyServesFIFO: while the standing delay stays below target
// the queue is healthy and serves oldest-first — the first freed slot goes to the
// head, the second-enqueued caller keeps waiting.
func TestBulkheadCoDelHealthyServesFIFO(t *testing.T) {
	t.Parallel()

	cc := newCodelClock()
	bh := r8e.NewBulkhead(1, cc, &r8e.Hooks{},
		r8e.BulkheadCoDel(codelTarget, codelInterval), r8e.BulkheadQueueDepth(4))

	require.NoError(t, bh.Acquire(t.Context())) // hold the slot

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	resA := startWaiter(ctx, t, bh, 1) // head
	resB := startWaiter(ctx, t, bh, 2) // tail

	// Dwell stays below target (2ms < 5ms): healthy, so FIFO — A is served first.
	cc.set(2 * time.Millisecond)
	bh.Release()

	require.NoError(t, <-resA)
	require.Equal(t, int64(1), bh.Queued()) // B still waiting → A (head) went first
	require.False(t, bh.Overloaded())

	cc.set(3 * time.Millisecond)
	bh.Release()
	require.NoError(t, <-resB)
}

// TestBulkheadCoDelShedsStaleAndServesLIFO drives the full controlled-delay path:
// once the standing delay has stayed above target for a full interval the queue
// is overloaded, the stale front waiters are shed with ErrCoDelShed, and the
// freed slot is handed to the NEWEST waiter (adaptive LIFO).
func TestBulkheadCoDelShedsStaleAndServesLIFO(t *testing.T) {
	t.Parallel()

	var shed atomic.Int64

	cc := newCodelClock()
	bh := r8e.NewBulkhead(1, cc, &r8e.Hooks{
		OnCoDelShed: func() { shed.Add(1) },
	}, r8e.BulkheadCoDel(codelTarget, codelInterval), r8e.BulkheadQueueDepth(10))

	require.NoError(t, bh.Acquire(t.Context())) // hold the slot

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Arm the overload timer: one caller queued long enough to push the standing
	// delay above target, then served (FIFO, not yet overloaded).
	cc.set(0)
	resArm := startWaiter(ctx, t, bh, 1)
	cc.set(10 * time.Millisecond) // standing 10ms > target → aboveSince armed
	bh.Release()
	require.NoError(t, <-resArm) // granted FIFO, holds the slot

	// Three stale callers (enqueued early) and three fresh ones (enqueued late).
	cc.set(15 * time.Millisecond)
	resStale := []<-chan error{
		startWaiter(ctx, t, bh, 1),
		startWaiter(ctx, t, bh, 2),
		startWaiter(ctx, t, bh, 3),
	}

	cc.set(118 * time.Millisecond)
	resE := startWaiter(ctx, t, bh, 4)
	resF := startWaiter(ctx, t, bh, 5)
	resFresh := startWaiter(ctx, t, bh, 6) // newest

	// 120ms: standing delay (oldest = 105ms) persisted above target a full
	// interval → overloaded latches; the three stale callers are shed and the
	// freed slot goes to the newest fresh caller (LIFO).
	cc.set(120 * time.Millisecond)
	bh.Release()

	for i, res := range resStale {
		require.ErrorIsf(t, <-res, r8e.ErrCoDelShed, "stale caller %d not shed", i)
	}

	require.NoError(t, <-resFresh)          // newest served first → adaptive LIFO
	require.Equal(t, int64(3), shed.Load()) // exactly the three stale callers
	require.Equal(t, int64(2), bh.Queued()) // E and F remain queued
	require.True(t, bh.Overloaded())        // latch holds while the queue is backed up

	// Standing delay now is the oldest remaining (E, enqueued 118ms): 2ms of 10ms
	// slough → load 0.2.
	require.InDelta(t, 0.2, bh.CoDelLoad(), 1e-9)

	cancel() // drain E and F
	assert.ErrorIs(t, <-resE, context.Canceled)
	assert.ErrorIs(t, <-resF, context.Canceled)
}

// TestBulkheadCoDelSloughBoundaryNotShed pins the slough boundary: a waiter whose
// dwell is EXACTLY the slough timeout (2 × target) is NOT shed — the comparison is
// strictly greater-than. Under overload it is therefore served, not dropped.
func TestBulkheadCoDelSloughBoundaryNotShed(t *testing.T) {
	t.Parallel()

	cc := newCodelClock()
	bh := r8e.NewBulkhead(1, cc, &r8e.Hooks{},
		r8e.BulkheadCoDel(codelTarget, codelInterval), r8e.BulkheadQueueDepth(4))

	require.NoError(t, bh.Acquire(t.Context())) // hold the slot

	// Arm the overload timer (above target since 10ms), then serve the arming
	// caller so the queue is empty for the boundary waiter.
	cc.set(0)
	arm := startWaiter(t.Context(), t, bh, 1)
	cc.set(10 * time.Millisecond)
	bh.Release()
	require.NoError(t, <-arm)

	// Enqueue the boundary waiter at 105ms so its dwell at the 115ms release is
	// exactly the slough timeout (10ms = 2 × 5ms target).
	cc.set(105 * time.Millisecond)
	res := startWaiter(t.Context(), t, bh, 1)

	// 115ms: standing delay (10ms > target) has persisted a full interval since
	// aboveSince=10ms (105ms >= 100ms) → overloaded latches. The boundary waiter's
	// dwell is exactly 10ms == slough, which is NOT > slough, so it survives the
	// shed pass and is served.
	cc.set(115 * time.Millisecond)
	bh.Release()

	require.NoError(t, <-res, "a waiter at exactly the slough timeout must be served, not shed")
}

// TestBulkheadCoDelLoadAndOverloadWhenDisabled: with CoDel off, the load gauge is
// 0 and the queue is never reported overloaded, regardless of occupancy.
func TestBulkheadCoDelLoadAndOverloadWhenDisabled(t *testing.T) {
	t.Parallel()

	cc := newCodelClock()
	bh := r8e.NewBulkhead(1, cc, &r8e.Hooks{}, r8e.BulkheadMaxWait(time.Hour))

	require.NoError(t, bh.Acquire(t.Context()))

	res := startWaiter(t.Context(), t, bh, 1)

	assert.Zero(t, bh.CoDelLoad())   // CoDel disabled → 0
	assert.False(t, bh.Overloaded()) // never overloaded without CoDel

	bh.Release()
	require.NoError(t, <-res)
}

// TestBulkheadCoDelNonPositiveIgnored: a non-positive target or interval leaves
// the controlled-delay discipline off, so a full bulkhead with no other wait
// rejects immediately rather than queueing.
func TestBulkheadCoDelNonPositiveIgnored(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name             string
		target, interval time.Duration
	}{
		{"zero target", 0, codelInterval},
		{"zero interval", codelTarget, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cc := newCodelClock()
			bh := r8e.NewBulkhead(1, cc, &r8e.Hooks{},
				r8e.BulkheadCoDel(tc.target, tc.interval))

			require.NoError(t, bh.Acquire(t.Context()))
			// CoDel disabled → no wait enabled → reject immediately, never queue.
			require.ErrorIs(t, bh.Acquire(t.Context()), r8e.ErrBulkheadFull)
			require.False(t, bh.Overloaded())
		})
	}
}

// TestBulkheadCoDelContextCancelWhileQueued: cancelling ctx while parked on a
// CoDel-only queue (no max-wait timer) returns the context error and drains it.
func TestBulkheadCoDelContextCancelWhileQueued(t *testing.T) {
	t.Parallel()

	cc := newCodelClock()
	bh := r8e.NewBulkhead(1, cc, &r8e.Hooks{},
		r8e.BulkheadCoDel(codelTarget, codelInterval))

	require.NoError(t, bh.Acquire(t.Context()))

	ctx, cancel := context.WithCancel(context.Background())
	res := startWaiter(ctx, t, bh, 1)

	cancel()

	require.ErrorIs(t, <-res, context.Canceled)
	require.Zero(t, bh.Queued())
}

// TestBulkheadReconfigureEnablesCoDel: Reconfigure can turn the controlled-delay
// discipline on at runtime, after which a full bulkhead queues callers.
func TestBulkheadReconfigureEnablesCoDel(t *testing.T) {
	t.Parallel()

	cc := newCodelClock()
	bh := r8e.NewBulkhead(1, cc, &r8e.Hooks{}) // no wait at all

	require.NoError(t, bh.Acquire(t.Context()))
	// Without a wait the bulkhead rejects immediately.
	require.ErrorIs(t, bh.Acquire(t.Context()), r8e.ErrBulkheadFull)

	bh.Reconfigure(1, r8e.BulkheadCoDel(codelTarget, codelInterval))

	// Now a full bulkhead queues instead of rejecting.
	res := startWaiter(t.Context(), t, bh, 1)
	require.Equal(t, int64(1), bh.Queued())

	bh.Release()
	require.NoError(t, <-res)
}

// TestBulkheadCoDelShedRacesCancel stresses the race between a controlled-delay
// shed handing the waiter ErrCoDelShed and the waiter's context cancellation:
// whichever wins, the outcome is ErrCoDelShed or context.Canceled and no slot
// leaks. It also exercises the ctx-cancel-after-shed branch of waitForSlot.
func TestBulkheadCoDelShedRacesCancel(t *testing.T) {
	t.Parallel()

	var shed atomic.Int64

	for range 200 {
		cc := newCodelClock()
		bh := r8e.NewBulkhead(1, cc, &r8e.Hooks{
			OnCoDelShed: func() { shed.Add(1) },
		},
			r8e.BulkheadCoDel(codelTarget, codelInterval),
			r8e.BulkheadQueueDepth(4))
		require.NoError(t, bh.Acquire(t.Context()))

		// Arm the overload timer with one caller, then serve it FIFO.
		cc.set(0)
		arm := startWaiter(t.Context(), t, bh, 1)
		cc.set(10 * time.Millisecond)
		bh.Release()
		require.NoError(t, <-arm)

		// Enqueue the victim, stale at the race instant.
		cc.set(15 * time.Millisecond)
		ctx, cancel := context.WithCancel(context.Background())

		res := make(chan error, 1)
		go func() { res <- bh.Acquire(ctx) }()
		require.Eventually(t, func() bool { return bh.Queued() == 1 },
			time.Second, time.Microsecond)

		// 120ms: the victim's dwell (105ms) latches overload and exceeds the
		// slough timeout, so a Release sheds it — raced against cancellation.
		cc.set(120 * time.Millisecond)

		before := shed.Load()

		var wg sync.WaitGroup

		wg.Add(2)

		go func() { defer wg.Done(); bh.Release() }()
		go func() { defer wg.Done(); cancel() }()
		wg.Wait()

		err := <-res
		delta := shed.Load() - before

		// The outcome and the OnCoDelShed hook are tied: a controlled-delay shed
		// returns ErrCoDelShed AND fires the hook exactly once (whichever select
		// arm wins — the <-ready path or the ctx-cancel-after-shed path); a victim
		// abandoned by cancellation first returns context.Canceled and fires no
		// shed hook. This pins both the error identity and the hook on the racy
		// branch, where the stress test previously accepted either outcome blindly.
		switch {
		case errors.Is(err, r8e.ErrCoDelShed):
			require.Equal(t, int64(1), delta, "a shed victim must fire OnCoDelShed once")
		case errors.Is(err, context.Canceled):
			require.Zero(t, delta, "a cancelled victim must not fire OnCoDelShed")
		default:
			t.Fatalf("unexpected victim outcome: %v", err)
		}

		require.Zero(t, bh.InUse())  // freed slot never leaks
		require.Zero(t, bh.Queued()) // victim no longer queued either way
	}
}

// TestPolicyCoDelShedMetric: the policy middleware wires the controlled-delay
// shed end to end — a queued call is shed with ErrCoDelShed and the CoDelShed
// counter and user hook reflect it.
func TestPolicyCoDelShedMetric(t *testing.T) {
	t.Parallel()

	var shedHook atomic.Int64

	cc := newCodelClock()
	p := r8e.NewPolicy[string]("codel",
		r8e.WithClock(cc),
		r8e.WithHooks(&r8e.Hooks{OnCoDelShed: func() { shedHook.Add(1) }}),
		r8e.WithBulkhead(1,
			r8e.BulkheadCoDel(codelTarget, codelInterval),
			r8e.BulkheadQueueDepth(4)),
	)

	// Hold the slot with a blocking call, releasing it on demand, so the freed
	// slot triggers the controlled-delay pass at a clock instant we control.
	hold1, holding1 := make(chan struct{}), make(chan struct{})
	cc.set(0)

	go func() {
		_, _ = p.Do(t.Context(), func(_ context.Context) (string, error) {
			close(holding1)
			<-hold1

			return "ok", nil
		})
	}()
	<-holding1

	// Arming caller: queues at t=0, becomes the next holder at t=10ms (FIFO).
	hold2, holding2 := make(chan struct{}), make(chan struct{})

	go func() {
		_, _ = p.Do(t.Context(), func(_ context.Context) (string, error) {
			close(holding2)
			<-hold2

			return "ok", nil
		})
	}()
	require.Eventually(t, func() bool { return p.Metrics().BulkheadQueued == 1 },
		time.Second, time.Millisecond)

	cc.set(10 * time.Millisecond)
	close(hold1) // first holder finishes → arming caller granted, standing armed
	<-holding2

	// Victim: queues at t=15ms, stale by the time the slot frees at t=120ms.
	victim := make(chan error, 1)
	cc.set(15 * time.Millisecond)

	go func() {
		_, err := p.Do(t.Context(), func(_ context.Context) (string, error) {
			return "ok", nil
		})
		victim <- err
	}()
	require.Eventually(t, func() bool { return p.Metrics().BulkheadQueued == 1 },
		time.Second, time.Millisecond)

	cc.set(120 * time.Millisecond)
	close(hold2) // arming caller finishes → overloaded latches → victim shed

	require.ErrorIs(t, <-victim, r8e.ErrCoDelShed)
	assert.Equal(t, int64(1), p.Metrics().CoDelShed)
	assert.Equal(t, int64(1), shedHook.Load())
}
