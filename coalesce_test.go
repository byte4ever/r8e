package r8e

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	waitTimeout = 2 * time.Second
	waitTick    = time.Millisecond
)

// ---------------------------------------------------------------------------
// Coalescer unit tests
// ---------------------------------------------------------------------------

// gate is a synchronisation handle for a coalesced function: it signals each
// time the function starts and blocks every invocation until the test releases
// them, while counting invocations.
type gate struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func newGate() *gate {
	return &gate{
		started: make(chan struct{}, 64),
		release: make(chan struct{}),
	}
}

// fn returns a coalesced function that records its invocation, signals start,
// then waits for release or its own context — returning val on release and the
// context error if its context is cancelled first. The context branch is the
// detachment probe: under a detached shared context it never fires.
func (g *gate) fn(val string) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		g.calls.Add(1)
		g.started <- struct{}{}

		select {
		case <-g.release:
			return val, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func TestCoalescerCollapsesConcurrentCalls(t *testing.T) {
	t.Parallel()

	const followers = 20

	var leaders, joined atomic.Int64

	hooks := &Hooks{
		OnCoalesceLeader:   func() { leaders.Add(1) },
		OnCoalesceFollower: func() { joined.Add(1) },
	}
	c := NewCoalescer[string](hooks)
	g := newGate()

	results := make(chan string, followers+1)

	// Launch the leader and wait until it is inside fn (key registered).
	go func() {
		v, err := c.Do(context.Background(), "k", g.fn("shared"))
		assert.NoError(t, err)
		results <- v
	}()
	<-g.started

	assert.Equal(t, 1, c.InFlight(), "one distinct key executing")

	// Pile up followers on the same key while the leader is blocked.
	for range followers {
		go func() {
			v, err := c.Do(context.Background(), "k", g.fn("unused"))
			assert.NoError(t, err)
			results <- v
		}()
	}

	// Wait until every follower has joined, then release the shared call.
	require.Eventually(t, func() bool { return joined.Load() == followers },
		waitTimeout, waitTick)
	close(g.release)

	for range followers + 1 {
		assert.Equal(t, "shared", <-results)
	}

	assert.Equal(t, int64(1), g.calls.Load(), "fn must run exactly once")
	assert.Equal(t, int64(1), leaders.Load())
	assert.Equal(t, int64(followers), joined.Load())
	assert.Equal(t, 0, c.InFlight(), "key cleared after completion")
}

func TestCoalescerDistinctKeysDoNotCollapse(t *testing.T) {
	t.Parallel()

	c := NewCoalescer[string](&Hooks{})
	g := newGate()

	results := make(chan string, 2)

	for _, key := range []string{"a", "b"} {
		go func() {
			v, _ := c.Do(context.Background(), key, g.fn(key))
			results <- v
		}()
	}

	// Both keys must start before either is released — proves no coalescing.
	<-g.started
	<-g.started
	assert.Equal(t, 2, c.InFlight())

	close(g.release)

	got := []string{<-results, <-results}
	assert.ElementsMatch(t, []string{"a", "b"}, got)
	assert.Equal(t, int64(2), g.calls.Load())
}

func TestCoalescerSequentialCallsRunEachTime(t *testing.T) {
	t.Parallel()

	// A Coalescer is not a cache: non-overlapping calls each execute.
	c := NewCoalescer[int](&Hooks{})

	var calls atomic.Int64

	run := func(context.Context) (int, error) {
		return int(calls.Add(1)), nil
	}

	v1, err1 := c.Do(context.Background(), "k", run)
	require.NoError(t, err1)
	v2, err2 := c.Do(context.Background(), "k", run)
	require.NoError(t, err2)

	assert.Equal(t, 1, v1)
	assert.Equal(t, 2, v2)
	assert.Equal(t, 0, c.InFlight())
}

func TestCoalescerSharesError(t *testing.T) {
	t.Parallel()

	var joined atomic.Int64

	c := NewCoalescer[string](&Hooks{
		OnCoalesceFollower: func() { joined.Add(1) },
	})
	g := newGate()
	wantErr := errors.New("downstream down")

	fn := func(context.Context) (string, error) {
		g.calls.Add(1)
		g.started <- struct{}{}
		<-g.release

		return "", wantErr
	}

	errs := make(chan error, 2)

	go func() { _, err := c.Do(context.Background(), "k", fn); errs <- err }()
	<-g.started

	go func() {
		_, err := c.Do(context.Background(), "k", g.fn("unused"))
		errs <- err
	}()
	require.Eventually(t, func() bool { return joined.Load() == 1 },
		waitTimeout, waitTick)
	close(g.release)

	require.ErrorIs(t, <-errs, wantErr)
	require.ErrorIs(t, <-errs, wantErr)
	assert.Equal(t, int64(1), g.calls.Load(), "only the leader runs fn")
}

// TestCoalescerLeaderCancelDoesNotPoisonFollowers is the core robustness check:
// the shared call runs under a detached context, so cancelling the leader's
// context returns ctx.Err() to the leader alone while the shared call completes
// and a follower with a live context still receives the real result.
func TestCoalescerLeaderCancelDoesNotPoisonFollowers(t *testing.T) {
	t.Parallel()

	var joined atomic.Int64

	c := NewCoalescer[string](&Hooks{
		OnCoalesceFollower: func() { joined.Add(1) },
	})
	g := newGate()

	leaderCtx, cancelLeader := context.WithCancel(context.Background())

	leaderErr := make(chan error, 1)

	go func() {
		_, err := c.Do(leaderCtx, "k", g.fn("ok"))
		leaderErr <- err
	}()
	<-g.started

	followerRes := make(chan string, 1)

	go func() {
		// Background context: this follower never cancels.
		v, err := c.Do(context.Background(), "k", g.fn("unused"))
		assert.NoError(t, err)
		followerRes <- v
	}()
	require.Eventually(t, func() bool { return joined.Load() == 1 },
		waitTimeout, waitTick)

	// Cancel the leader: it must bail, the shared call must keep running.
	cancelLeader()
	require.ErrorIs(t, <-leaderErr, context.Canceled)

	// The shared call is still alive (detached): releasing it yields the real
	// result to the follower.
	close(g.release)
	assert.Equal(t, "ok", <-followerRes)
	assert.Equal(t, int64(1), g.calls.Load())
}

// TestCoalescerFollowerCancelLeavesGroupIntact is the mirror: a follower
// cancelling its own context bails out without disturbing the leader's result.
func TestCoalescerFollowerCancelLeavesGroupIntact(t *testing.T) {
	t.Parallel()

	var joined atomic.Int64

	c := NewCoalescer[string](&Hooks{
		OnCoalesceFollower: func() { joined.Add(1) },
	})
	g := newGate()

	leaderRes := make(chan string, 1)

	go func() {
		v, err := c.Do(context.Background(), "k", g.fn("ok"))
		assert.NoError(t, err)
		leaderRes <- v
	}()
	<-g.started

	followerCtx, cancelFollower := context.WithCancel(context.Background())

	followerErr := make(chan error, 1)

	go func() {
		_, err := c.Do(followerCtx, "k", g.fn("unused"))
		followerErr <- err
	}()
	require.Eventually(t, func() bool { return joined.Load() == 1 },
		waitTimeout, waitTick)

	// The follower abandons its wait; the leader is unaffected.
	cancelFollower()
	require.ErrorIs(t, <-followerErr, context.Canceled)

	close(g.release)
	assert.Equal(t, "ok", <-leaderRes)
	assert.Equal(t, int64(1), g.calls.Load())
}

func TestCoalescerInFlightEmptyByDefault(t *testing.T) {
	t.Parallel()

	c := NewCoalescer[string](&Hooks{})
	assert.Equal(t, 0, c.InFlight())
}

// TestCoalesceAwaitPrefersReadyResultOverCancelledCtx pins the tie-break: when
// the shared call has completed AND the caller's ctx is cancelled, await returns
// the result, never ctx.Err(). A bare select would resolve this at random, so
// the loop would flake if the deterministic pre-check regressed.
func TestCoalesceAwaitPrefersReadyResultOverCancelledCtx(t *testing.T) {
	t.Parallel()

	call := &coalesceCall[string]{done: make(chan struct{})}
	call.val, call.err = "done", nil
	close(call.done)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	for range 1000 {
		v, err := call.await(ctx)
		require.NoError(t, err)
		require.Equal(t, "done", v)
	}
}

// ---------------------------------------------------------------------------
// Policy integration: WithCoalesce
// ---------------------------------------------------------------------------

func TestWithCoalesceNilKeyFuncPanics(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t, ErrCoalesceNilKeyFunc, func() {
		_ = NewPolicy[string]("p", WithCoalesce(nil))
	})
}

func TestWithCoalesceWithoutTimeoutPanics(t *testing.T) {
	t.Parallel()

	// Coalescing's detached shared call needs a timeout to bound it; building a
	// coalescing policy without one is a loud misconfiguration.
	assert.PanicsWithValue(t, ErrCoalesceWithoutTimeout, func() {
		_ = NewPolicy[string]("p", WithCoalesce(keyFromCtx))
	})
}

func TestWithCoalesceEmptyKeySkipsCoalescing(t *testing.T) {
	t.Parallel()

	// keyFn returning "" opts every call out: concurrent calls each run fn.
	var leaders, followers atomic.Int64

	policy := NewPolicy[string]("p",
		WithTimeout(5*time.Second),
		WithHooks(&Hooks{
			OnCoalesceLeader:   func() { leaders.Add(1) },
			OnCoalesceFollower: func() { followers.Add(1) },
		}),
		WithCoalesce(func(context.Context) string { return "" }),
	)
	g := newGate()

	results := make(chan string, 2)
	for range 2 {
		go func() {
			v, _ := policy.Do(context.Background(), g.fn("v"))
			results <- v
		}()
	}

	<-g.started
	<-g.started // both ran concurrently: no coalescing happened
	close(g.release)

	<-results
	<-results
	assert.Equal(t, int64(2), g.calls.Load())
	assert.Zero(t, leaders.Load())
	assert.Zero(t, followers.Load())
}

func TestWithCoalescePolicyCollapsesByKey(t *testing.T) {
	t.Parallel()

	const callers = 10

	var leaderHook, followerHook atomic.Int64

	policy := NewPolicy[string]("coalesce-svc",
		WithTimeout(5*time.Second),
		WithHooks(&Hooks{
			OnCoalesceLeader:   func() { leaderHook.Add(1) },
			OnCoalesceFollower: func() { followerHook.Add(1) },
		}),
		WithCoalesce(keyFromCtx),
	)
	g := newGate()

	ctx := context.WithValue(context.Background(), testKey{}, "hot")

	results := make(chan string, callers)

	go func() {
		v, err := policy.Do(ctx, g.fn("shared"))
		assert.NoError(t, err)
		results <- v
	}()
	<-g.started

	for range callers - 1 {
		go func() {
			v, err := policy.Do(ctx, g.fn("unused"))
			assert.NoError(t, err)
			results <- v
		}()
	}

	require.Eventually(t, func() bool {
		return policy.Metrics().CoalesceFollowers == callers-1
	}, waitTimeout, waitTick)
	close(g.release)

	for range callers {
		assert.Equal(t, "shared", <-results)
	}

	m := policy.Metrics()
	assert.Equal(t, int64(1), m.CoalesceLeaders)
	assert.Equal(t, int64(callers-1), m.CoalesceFollowers)
	assert.Equal(t, int64(0), m.CoalesceInFlight)
	assert.Equal(t, int64(1), g.calls.Load())

	// User-supplied hooks fire alongside the metric counters.
	assert.Equal(t, int64(1), leaderHook.Load())
	assert.Equal(t, int64(callers-1), followerHook.Load())
}

// TestWithCoalesceCollapsesBeforeBulkhead proves coalescing sits outside the
// bulkhead: many concurrent same-key calls take a single bulkhead slot, so a
// bulkhead of 1 admits them all instead of rejecting the duplicates.
func TestWithCoalesceCollapsesBeforeBulkhead(t *testing.T) {
	t.Parallel()

	const callers = 8

	var followers atomic.Int64

	policy := NewPolicy[string]("coalesce-bulkhead",
		WithTimeout(5*time.Second),
		WithHooks(&Hooks{OnCoalesceFollower: func() { followers.Add(1) }}),
		WithCoalesce(keyFromCtx),
		WithBulkhead(1),
	)
	g := newGate()

	ctx := context.WithValue(context.Background(), testKey{}, "hot")

	results := make(chan error, callers)

	go func() {
		_, err := policy.Do(ctx, g.fn("shared"))
		results <- err
	}()
	<-g.started

	for range callers - 1 {
		go func() {
			_, err := policy.Do(ctx, g.fn("unused"))
			results <- err
		}()
	}

	require.Eventually(t, func() bool { return followers.Load() == callers-1 },
		waitTimeout, waitTick)
	close(g.release)

	for range callers {
		assert.NoError(t, <-results)
	}

	assert.Equal(t, int64(0), policy.Metrics().BulkheadRejected,
		"coalesced followers must not consume bulkhead slots")
	assert.Equal(t, int64(1), g.calls.Load())
}

// testKey is the context key carrying the coalescing key in tests.
type testKey struct{}

func keyFromCtx(ctx context.Context) string {
	if v, ok := ctx.Value(testKey{}).(string); ok {
		return v
	}

	return ""
}
