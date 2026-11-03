package r8e

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errShutdown = errors.New("client shutting down")

type baseTestKey struct{}

// spyBase is a base context that counts the AfterFunc links still registered
// on it. Its Value hides the inner cancelCtx, so context.AfterFunc goes
// through its AfterFunc method instead of linking to the inner context
// directly.
type spyBase struct {
	context.Context

	live atomic.Int64
}

func (s *spyBase) Value(any) any { return nil }

func (s *spyBase) AfterFunc(f func()) func() bool {
	s.live.Add(1)

	stop := context.AfterFunc(s.Context, f)

	return func() bool {
		if !stop() {
			return false
		}

		s.live.Add(-1)

		return true
	}
}

// causeOf returns the error stored in v, or nil when v holds none.
func causeOf(v *atomic.Value) error {
	err, _ := v.Load().(error)

	return err
}

// A detached context keeps the caller's values and ignores the caller's
// cancellation; with a base, it ends when the base ends, with the base's
// cause.
func TestDetachedContext(t *testing.T) {
	t.Parallel()

	t.Run("no base", func(t *testing.T) {
		t.Parallel()

		caller, cancelCaller := context.WithCancel(context.WithValue(t.Context(), baseTestKey{}, "v"))
		detached, stop := detachedContext(caller, nil)
		defer stop()

		cancelCaller()

		require.NoError(t, detached.Err(), "the caller's cancellation does not reach it")
		assert.Equal(t, "v", detached.Value(baseTestKey{}))
	})

	t.Run("base ends later", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			base, cancelBase := context.WithCancelCause(t.Context())
			caller, cancelCaller := context.WithCancel(context.WithValue(t.Context(), baseTestKey{}, "v"))

			detached, stop := detachedContext(caller, base)
			defer stop()

			cancelCaller()
			synctest.Wait()
			require.NoError(t, detached.Err(), "the caller's cancellation does not reach it")

			cancelBase(errShutdown)
			<-detached.Done()

			require.ErrorIs(t, detached.Err(), context.Canceled)
			require.ErrorIs(t, context.Cause(detached), errShutdown)
			assert.Equal(t, "v", detached.Value(baseTestKey{}))
		})
	})

	t.Run("base already ended", func(t *testing.T) {
		t.Parallel()

		base, cancelBase := context.WithCancelCause(t.Context())
		cancelBase(errShutdown)

		detached, stop := detachedContext(t.Context(), base)
		defer stop()

		require.ErrorIs(t, detached.Err(), context.Canceled, "ended before the call starts")
		require.ErrorIs(t, context.Cause(detached), errShutdown)
	})

	t.Run("stop releases the link", func(t *testing.T) {
		t.Parallel()

		inner, cancelInner := context.WithCancel(t.Context())
		defer cancelInner()

		base := &spyBase{Context: inner}

		detached, stop := detachedContext(t.Context(), base)
		require.EqualValues(t, 1, base.live.Load(), "the call is linked to the base")

		stop()

		require.ErrorIs(t, detached.Err(), context.Canceled, "stop ends the detached context")
		assert.Zero(t, base.live.Load(), "stop removes the link from the base")
	})
}

// Cancelling the base context stops a coalesced call still running after its
// caller left; the call keeps the caller's values.
func TestWithBaseContextStopsTheCoalescedCall(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		base, cancelBase := context.WithCancelCause(t.Context())

		p := NewPolicy[string]("",
			WithBaseContext(base),
			WithCoalesce(func(context.Context) string { return "k" }),
			WithTimeout(time.Hour),
		)

		var (
			seenValue atomic.Value
			seenCause atomic.Value
		)

		started := make(chan struct{})
		shared := func(ctx context.Context) (string, error) {
			if v, ok := ctx.Value(baseTestKey{}).(string); ok {
				seenValue.Store(v)
			}

			close(started)
			<-ctx.Done()
			seenCause.Store(context.Cause(ctx))

			return "", ctx.Err()
		}

		leaderCtx, cancelLeader := context.WithCancel(context.WithValue(t.Context(), baseTestKey{}, "leader"))

		leaderErr := make(chan error, 1)

		go func() {
			_, err := p.Do(leaderCtx, shared)
			leaderErr <- err
		}()

		<-started

		followerErr := make(chan error, 1)

		go func() {
			_, err := p.Do(t.Context(), failIfCalled(t))
			followerErr <- err
		}()

		synctest.Wait()

		cancelLeader()
		require.ErrorIs(t, <-leaderErr, context.Canceled, "the leader leaves")

		synctest.Wait()
		require.NoError(t, causeOf(&seenCause), "the leader leaving does not stop the shared call")

		cancelBase(errShutdown)

		require.ErrorIs(t, <-followerErr, context.Canceled, "the follower gets the stopped call's end")
		synctest.Wait()
		require.ErrorIs(t, causeOf(&seenCause), errShutdown)
		assert.Equal(t, "leader", seenValue.Load())
	})
}

// Cancelling the base context stops a refresh-ahead reload: the entry keeps
// its value and no refresh is recorded. The reload keeps the caller's values.
func TestWithBaseContextStopsTheRefresh(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var counters cacheCounters

		base, cancelBase := context.WithCancelCause(t.Context())

		p := NewPolicy[string]("",
			WithBaseContext(base),
			WithCache(newMemCache[CacheEntry[string]](), cacheKeyOf, time.Hour, RefreshAhead(30*time.Minute)),
			WithTimeout(time.Hour),
			WithHooks(counters.hooks()),
		)

		ctx := withKeys(context.WithValue(context.Background(), baseTestKey{}, "caller"), "k", "")
		_, err := p.Do(ctx, constFn("v1", new(atomic.Int64)))
		require.NoError(t, err)

		time.Sleep(40 * time.Minute)

		var (
			seenValue atomic.Value
			seenCause atomic.Value
		)

		v, err := p.Do(ctx, func(ctx context.Context) (string, error) {
			if v, ok := ctx.Value(baseTestKey{}).(string); ok {
				seenValue.Store(v)
			}

			<-ctx.Done()
			seenCause.Store(context.Cause(ctx))

			return "", ctx.Err()
		})
		require.NoError(t, err)
		require.Equal(t, "v1", v, "the in-window read is served from the cache")

		synctest.Wait()
		require.NoError(t, causeOf(&seenCause), "the reload is running")
		assert.Equal(t, "caller", seenValue.Load(), "the reload keeps the caller's values")

		cancelBase(errShutdown)
		synctest.Wait()

		require.ErrorIs(t, causeOf(&seenCause), errShutdown)
		assert.Zero(t, counters.refreshed.Load())

		// This read is still in the refresh window, so it starts a reload
		// under the ended base: that reload must end before fn runs.
		var calls atomic.Int64

		v, err = p.Do(ctx, constFn("unexpected", &calls))
		require.NoError(t, err)
		assert.Equal(t, "v1", v, "a stopped reload leaves the entry alone")

		synctest.Wait()
		assert.Zero(t, calls.Load(), "a reload under an ended base never calls fn")
	})
}

// A nil base context is ignored: it does not clear a base set before it.
func TestWithBaseContextIgnoresNil(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		base, cancelBase := context.WithCancelCause(t.Context())

		p := NewPolicy[string]("",
			WithBaseContext(base),
			WithBaseContext(nil), //nolint:staticcheck // nil is the case under test
			WithCoalesce(func(context.Context) string { return "k" }),
			WithTimeout(time.Hour),
		)

		var seenCause atomic.Value

		done := make(chan error, 1)

		go func() {
			_, err := p.Do(t.Context(), func(ctx context.Context) (string, error) {
				<-ctx.Done()
				seenCause.Store(context.Cause(ctx))

				return "", ctx.Err()
			})
			done <- err
		}()

		synctest.Wait()
		cancelBase(errShutdown)

		require.ErrorIs(t, <-done, context.Canceled)
		require.ErrorIs(t, causeOf(&seenCause), errShutdown, "the first base still stops the call")
	})
}

// Once the base has ended, a coalesced call fails at once for any holder,
// even one whose own context is live, while a call with no coalescing key
// still runs.
func TestWithBaseContextEndedFailsLaterCoalescedCalls(t *testing.T) {
	t.Parallel()

	base, cancelBase := context.WithCancelCause(t.Context())
	cancelBase(errShutdown)

	p := NewPolicy[string]("",
		WithBaseContext(base),
		WithCoalesce(coalesceKeyOf),
		WithTimeout(time.Hour),
	)

	var calls atomic.Int64

	_, err := p.Do(withKeys(t.Context(), "", "k"), constFn("fresh", &calls))
	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, calls.Load(), "the coalesced call ends before fn runs")

	v, err := p.Do(withKeys(t.Context(), "", ""), constFn("fresh", &calls))
	require.NoError(t, err)
	assert.Equal(t, "fresh", v, "a call with no coalescing key runs on its caller's context")
	assert.EqualValues(t, 1, calls.Load())
}

// A coalesced miss the base stopped is not cached negatively: a later call
// with the same cache key, on this policy or on another one sharing the
// cache, reaches the downstream.
func TestWithBaseContextEndedCachesNoFailure(t *testing.T) {
	t.Parallel()

	tests := map[string]bool{
		"same policy":                false,
		"other policy, shared cache": true,
	}

	for name, otherPolicy := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				mem := newMemCache[CacheEntry[string]]()
				base, cancelBase := context.WithCancelCause(t.Context())

				p := NewPolicy[string]("",
					WithBaseContext(base),
					WithCoalesce(coalesceKeyOf),
					WithCache(mem, cacheKeyOf, time.Hour, NegativeCache(time.Hour)),
					WithTimeout(time.Hour),
				)

				leaderErr := make(chan error, 1)

				go func() {
					_, err := p.Do(withKeys(t.Context(), "k", "k"), func(ctx context.Context) (string, error) {
						<-ctx.Done()

						return "", ctx.Err()
					})
					leaderErr <- err
				}()

				synctest.Wait()
				cancelBase(errShutdown)
				require.ErrorIs(t, <-leaderErr, context.Canceled, "the caller's own context is live")

				next := p
				if otherPolicy {
					next = NewPolicy[string]("", WithCache(mem, cacheKeyOf, time.Hour, NegativeCache(time.Hour)))
				}

				var calls atomic.Int64

				v, err := next.Do(withKeys(t.Context(), "k", ""), constFn("fresh", &calls))
				require.NoError(t, err)
				assert.Equal(t, "fresh", v)
				assert.EqualValues(t, 1, calls.Load(), "the downstream is reached")
			})
		})
	}
}
