package r8e_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
)

// okCall is an operation that succeeds at once.
func okCall(context.Context) (string, error) { return "ok", nil }

// A queued call that waits the full max wait of a shared bulkhead is reported
// to the policy it went through, not to the hooks the bulkhead was built with.
func TestWithSharedBulkheadReportsTimeoutsToThePolicy(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var own, route atomic.Int64

		shared := r8e.NewBulkhead(1, r8e.RealClock{},
			&r8e.Hooks{OnBulkheadTimeout: func() { own.Add(1) }},
			r8e.BulkheadMaxWait(time.Second))
		require.NoError(t, shared.Acquire(t.Context()))

		defer shared.Release()

		p := r8e.NewPolicy[string]("", r8e.WithSharedBulkhead(shared),
			r8e.WithHooks(&r8e.Hooks{OnBulkheadTimeout: func() { route.Add(1) }}))

		_, err := p.Do(t.Context(), okCall)
		require.ErrorIs(t, err, r8e.ErrBulkheadTimeout)

		assert.Equal(t, int64(1), p.Metrics().BulkheadTimeouts, "policy metric")
		assert.Equal(t, int64(1), route.Load(), "policy hook")
		assert.Zero(t, own.Load(), "the bulkhead's own hook")
	})
}

// The max wait of a shared bulkhead runs on the clock given to NewBulkhead,
// not on the policy's WithClock: with a frozen policy clock the call still
// times out after the bulkhead's own second.
func TestWithSharedBulkheadKeepsItsOwnClock(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		shared := r8e.NewBulkhead(1, r8e.RealClock{}, &r8e.Hooks{}, r8e.BulkheadMaxWait(time.Second))
		require.NoError(t, shared.Acquire(t.Context()))

		defer shared.Release()

		p := r8e.NewPolicy[string]("", r8e.WithClock(&manualClock{}), r8e.WithSharedBulkhead(shared))

		start := time.Now()

		_, err := p.Do(t.Context(), okCall)
		require.ErrorIs(t, err, r8e.ErrBulkheadTimeout)
		assert.Equal(t, time.Second, time.Since(start), "timed on the bulkhead's clock")
	})
}

// A queued call shed by the controlled-delay discipline of a shared bulkhead is
// reported to the policy it went through.
func TestWithSharedBulkheadReportsCoDelShedToThePolicy(t *testing.T) {
	t.Parallel()

	var own, route atomic.Int64

	cc := newCodelClock()
	shared := r8e.NewBulkhead(1, cc, &r8e.Hooks{OnCoDelShed: func() { own.Add(1) }},
		r8e.BulkheadCoDel(codelTarget, codelInterval),
		r8e.BulkheadQueueDepth(4))
	p := r8e.NewPolicy[string]("", r8e.WithSharedBulkhead(shared),
		r8e.WithHooks(&r8e.Hooks{OnCoDelShed: func() { route.Add(1) }}))

	// The same sequence as TestPolicyCoDelShedMetric: a holder, an arming
	// caller served FIFO, then a victim stale by the time the slot frees.
	hold1, holding1 := make(chan struct{}), make(chan struct{})
	cc.set(0)

	go func() {
		_, _ = p.Do(t.Context(), func(context.Context) (string, error) {
			close(holding1)
			<-hold1

			return "ok", nil
		})
	}()
	<-holding1

	hold2, holding2 := make(chan struct{}), make(chan struct{})

	go func() {
		_, _ = p.Do(t.Context(), func(context.Context) (string, error) {
			close(holding2)
			<-hold2

			return "ok", nil
		})
	}()
	require.Eventually(t, func() bool { return p.Metrics().BulkheadQueued == 1 },
		time.Second, time.Millisecond)

	cc.set(10 * time.Millisecond)
	close(hold1)
	<-holding2

	victim := make(chan error, 1)
	cc.set(15 * time.Millisecond)

	go func() {
		_, err := p.Do(t.Context(), okCall)
		victim <- err
	}()
	require.Eventually(t, func() bool { return p.Metrics().BulkheadQueued == 1 },
		time.Second, time.Millisecond)

	cc.set(120 * time.Millisecond)
	close(hold2)

	require.ErrorIs(t, <-victim, r8e.ErrCoDelShed)
	assert.Equal(t, int64(1), p.Metrics().CoDelShed, "policy metric")
	assert.Equal(t, int64(1), route.Load(), "policy hook")
	assert.Zero(t, own.Load(), "the bulkhead's own hook")
}

// When a controlled-delay shed races the waiter's cancellation, a shed victim
// that went through a policy reports the shed to that policy, whichever arm of
// the wait wins.
func TestWithSharedBulkheadReportsShedRacesToThePolicy(t *testing.T) {
	t.Parallel()

	var own, route atomic.Int64

	for range 200 {
		cc := newCodelClock()
		shared := r8e.NewBulkhead(1, cc, &r8e.Hooks{OnCoDelShed: func() { own.Add(1) }},
			r8e.BulkheadCoDel(codelTarget, codelInterval),
			r8e.BulkheadQueueDepth(4))
		p := r8e.NewPolicy[string]("", r8e.WithSharedBulkhead(shared),
			r8e.WithHooks(&r8e.Hooks{OnCoDelShed: func() { route.Add(1) }}))

		require.NoError(t, shared.Acquire(t.Context()))

		cc.set(0)
		arm := startWaiter(t.Context(), t, shared, 1)
		cc.set(10 * time.Millisecond)
		shared.Release()
		require.NoError(t, <-arm)

		cc.set(15 * time.Millisecond)

		ctx, cancel := context.WithCancel(context.Background())
		res := make(chan error, 1)

		go func() {
			_, err := p.Do(ctx, okCall)
			res <- err
		}()
		require.Eventually(t, func() bool { return shared.Queued() == 1 },
			time.Second, time.Microsecond)

		cc.set(120 * time.Millisecond)

		before := route.Load()

		var wg sync.WaitGroup

		wg.Add(2)

		go func() { defer wg.Done(); shared.Release() }()
		go func() { defer wg.Done(); cancel() }()
		wg.Wait()

		err := <-res
		delta := route.Load() - before

		switch {
		case errors.Is(err, r8e.ErrCoDelShed):
			require.Equal(t, int64(1), delta, "a shed victim reports to its policy once")
		case errors.Is(err, context.Canceled):
			require.Zero(t, delta, "a cancelled victim reports no shed")
		default:
			t.Fatalf("unexpected victim outcome: %v", err)
		}

		require.Zero(t, shared.InUse())
		require.Zero(t, shared.Queued())
	}

	assert.Zero(t, own.Load(), "the bulkhead's own hook sees no policy shed")
}
