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

		synctest.Test(t, func(t *testing.T) {
			base, cancelBase := context.WithCancelCause(t.Context())

			detached, stop := detachedContext(t.Context(), base)
			stop()

			cancelBase(errShutdown)
			synctest.Wait()

			require.ErrorIs(t, detached.Err(), context.Canceled, "stop ends the detached context")
			require.NotErrorIs(t, context.Cause(detached), errShutdown, "the base no longer reaches it")
		})
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
			seenValue.Store(ctx.Value(baseTestKey{}))
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
		require.Nil(t, seenCause.Load(), "the leader leaving does not stop the shared call")

		cancelBase(errShutdown)

		require.ErrorIs(t, <-followerErr, context.Canceled, "the follower gets the stopped call's end")
		synctest.Wait()
		assert.Equal(t, errShutdown, seenCause.Load())
		assert.Equal(t, "leader", seenValue.Load())
	})
}

// Cancelling the base context stops a refresh-ahead reload: the entry keeps
// its value and no refresh is recorded.
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

		ctx := withKeys(context.Background(), "k", "")
		_, err := p.Do(ctx, constFn("v1", new(atomic.Int64)))
		require.NoError(t, err)

		time.Sleep(40 * time.Minute)

		var seenCause atomic.Value

		v, err := p.Do(ctx, func(ctx context.Context) (string, error) {
			<-ctx.Done()
			seenCause.Store(context.Cause(ctx))

			return "", ctx.Err()
		})
		require.NoError(t, err)
		require.Equal(t, "v1", v, "the in-window read is served from the cache")

		synctest.Wait()
		require.Nil(t, seenCause.Load(), "the reload is running")

		cancelBase(errShutdown)
		synctest.Wait()

		assert.Equal(t, errShutdown, seenCause.Load())
		assert.Zero(t, counters.refreshed.Load())

		v, err = p.Do(ctx, failIfCalled(t))
		require.NoError(t, err)
		assert.Equal(t, "v1", v, "a stopped reload leaves the entry alone")
	})
}

// A nil base context is ignored.
func TestWithBaseContextIgnoresNil(t *testing.T) {
	t.Parallel()

	var setup policySetup

	WithBaseContext(nil).apply(&setup) //nolint:staticcheck // nil is the case under test

	assert.Nil(t, setup.detach)
}
