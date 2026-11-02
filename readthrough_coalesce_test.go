package r8e

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCachedCoalescedPolicy builds a policy with a cache in front of
// coalescing, both keyed on one constant key, and counts the cache events.
func newCachedCoalescedPolicy(counters *cacheCounters) *Policy[string] {
	keyFn := func(context.Context) string { return "k" }

	return NewPolicy[string](
		"",
		WithCache(newMemCache[CacheEntry[string]](), keyFn, time.Hour),
		WithCoalesce(keyFn),
		WithTimeout(10*time.Second),
		WithHooks(counters.hooks()),
	)
}

// slowFn returns a downstream call that takes d, then succeeds with val, and
// counts its invocations.
func slowFn(d time.Duration, val string, calls *atomic.Int64) func(context.Context) (string, error) {
	return func(ctx context.Context) (string, error) {
		calls.Add(1)

		select {
		case <-time.After(d):
			return val, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

// The coalesced call outlives a caller that gave up on it. Its result must
// still reach the cache, so the next call is a hit instead of a second trip
// downstream.
func TestWithCacheCoalesceStoresAfterEveryCallerLeft(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var (
			counters cacheCounters
			calls    atomic.Int64
		)

		p := newCachedCoalescedPolicy(&counters)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := p.Do(ctx, slowFn(2*time.Second, "token", &calls))
		require.ErrorIs(t, err, context.DeadlineExceeded)

		// Let the detached call finish.
		time.Sleep(2 * time.Second)
		synctest.Wait()

		v, err := p.Do(context.Background(), slowFn(2*time.Second, "other", &calls))
		require.NoError(t, err)

		assert.Equal(t, "token", v)
		assert.Equal(t, int64(1), calls.Load(), "one trip downstream")
		assert.Equal(t, int64(1), counters.hits.Load())
		assert.Equal(t, int64(1), counters.stores.Load())
	})
}

// When the caller is still waiting, the result is stored once, not once by
// the coalesced call and again by the caller.
func TestWithCacheCoalesceStoresOnceWhileCallerWaits(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var (
			counters cacheCounters
			calls    atomic.Int64
		)

		p := newCachedCoalescedPolicy(&counters)

		v, err := p.Do(context.Background(), slowFn(2*time.Second, "token", &calls))
		require.NoError(t, err)

		assert.Equal(t, "token", v)
		assert.Equal(t, int64(1), counters.stores.Load())
	})
}

// A failed coalesced call stores nothing, even when its caller has left.
func TestWithCacheCoalesceFailureStoresNothing(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var counters cacheCounters

		p := newCachedCoalescedPolicy(&counters)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := p.Do(ctx, func(context.Context) (string, error) {
			time.Sleep(2 * time.Second)

			return "", errDownstream
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)

		time.Sleep(2 * time.Second)
		synctest.Wait()

		assert.Zero(t, counters.stores.Load())
	})
}

// A coalesced call only fills the cache of its own policy. Here the inner
// policy opts this call out of its cache (empty cache key) but coalesces it;
// the outer policy times out. The inner coalesced call then succeeds after the
// outer chain has failed, and must not write into the outer cache.
func TestWithCacheCoalesceNestedPolicyDoesNotStoreIntoOuterCache(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var (
			outer cacheCounters
			calls atomic.Int64
		)

		keyFn := func(context.Context) string { return "k" }
		noCacheKey := func(context.Context) string { return "" }

		inner := NewPolicy[string](
			"",
			WithCache(newMemCache[CacheEntry[string]](), noCacheKey, time.Hour),
			WithCoalesce(keyFn),
			WithTimeout(10*time.Second),
		)
		outerPolicy := NewPolicy[string](
			"",
			WithCache(newMemCache[CacheEntry[string]](), keyFn, time.Hour),
			WithTimeout(time.Second),
			WithHooks(outer.hooks()),
		)

		_, err := outerPolicy.Do(context.Background(), func(ctx context.Context) (string, error) {
			return inner.Do(ctx, slowFn(2*time.Second, "token", &calls))
		})
		require.ErrorIs(t, err, ErrTimeout)

		time.Sleep(2 * time.Second)
		synctest.Wait()

		assert.Equal(t, int64(1), calls.Load())
		assert.Zero(t, outer.stores.Load(), "the outer chain failed, so the outer cache stays empty")
	})
}

// callKeys carries the cache key and the coalescing key of one call, so a test
// can give them different values.
type callKeys struct {
	cache, coalesce string
}

type callKeysKey struct{}

func withKeys(ctx context.Context, cache, coalesce string) context.Context {
	return context.WithValue(ctx, callKeysKey{}, callKeys{cache: cache, coalesce: coalesce})
}

func cacheKeyOf(ctx context.Context) string {
	keys, _ := ctx.Value(callKeysKey{}).(callKeys)

	return keys.cache
}

func coalesceKeyOf(ctx context.Context) string {
	keys, _ := ctx.Value(callKeysKey{}).(callKeys)

	return keys.coalesce
}

// failIfCalled is a downstream call for reads that must be cache hits.
func failIfCalled(t *testing.T) func(context.Context) (string, error) {
	t.Helper()

	return func(context.Context) (string, error) {
		t.Error("downstream called on a read that should hit the cache")

		return "", errDownstream
	}
}

// A function that calls its own policy again, opting out of the cache but not
// of coalescing, must not see the outer miss's cache write: the outer key keeps
// the outer result.
func TestWithCacheCoalesceReentrantOptOutKeepsOuterValue(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := NewPolicy[string](
			"",
			WithCache(newMemCache[CacheEntry[string]](), cacheKeyOf, time.Hour),
			WithCoalesce(coalesceKeyOf),
			WithTimeout(10*time.Second),
		)

		outer, err := p.Do(withKeys(context.Background(), "a", "ca"), func(ctx context.Context) (string, error) {
			inner, err := p.Do(withKeys(ctx, "", "cb"), func(context.Context) (string, error) {
				return "inner", nil
			})

			return "outer(" + inner + ")", err
		})
		require.NoError(t, err)
		require.Equal(t, "outer(inner)", outer)

		v, err := p.Do(withKeys(context.Background(), "a", "ca"), failIfCalled(t))
		require.NoError(t, err)
		assert.Equal(t, "outer(inner)", v)
	})
}

// A function that calls its own policy again on a key in its refresh-ahead
// window starts a detached reload. The reload must not settle the outer miss's
// cache write, even when it finishes before the outer function returns.
func TestWithCacheCoalesceReentrantRefreshAheadKeepsOuterValue(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var counters cacheCounters

		keyFn := cacheKeyOf
		p := NewPolicy[string](
			"",
			WithCache(newMemCache[CacheEntry[string]](), keyFn, time.Hour, RefreshAhead(30*time.Minute)),
			WithCoalesce(keyFn),
			WithTimeout(10*time.Second),
			WithHooks(counters.hooks()),
		)

		ctxB := withKeys(context.Background(), "b", "")
		_, err := p.Do(ctxB, constFn("b-v1", new(atomic.Int64)))
		require.NoError(t, err)

		time.Sleep(40 * time.Minute)

		outer, err := p.Do(withKeys(context.Background(), "a", ""), func(ctx context.Context) (string, error) {
			b, err := p.Do(withKeys(ctx, "b", ""), constFn("b-v2", new(atomic.Int64)))

			// Let the detached reload of "b" finish first.
			time.Sleep(time.Second)

			return "a(" + b + ")", err
		})
		require.NoError(t, err)
		require.Equal(t, "a(b-v1)", outer)

		synctest.Wait()
		require.Equal(t, int64(1), counters.refreshed.Load())

		a, err := p.Do(withKeys(context.Background(), "a", ""), failIfCalled(t))
		require.NoError(t, err)
		assert.Equal(t, "a(b-v1)", a, "the outer key keeps the outer result")

		b, err := p.Do(ctxB, failIfCalled(t))
		require.NoError(t, err)
		assert.Equal(t, "b-v2", b, "the reload refreshed its own key")
	})
}

// With one keyFn for the cache and coalescing, a refresh-ahead reload goes
// through the coalescing layer with no cache write to settle. It refreshes the
// entry as usual.
func TestWithCacheCoalesceRefreshAheadReloads(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var counters cacheCounters

		keyFn := func(context.Context) string { return "k" }
		p := NewPolicy[string](
			"",
			WithCache(newMemCache[CacheEntry[string]](), keyFn, time.Hour, RefreshAhead(30*time.Minute)),
			WithCoalesce(keyFn),
			WithTimeout(10*time.Second),
			WithHooks(counters.hooks()),
		)

		_, err := p.Do(context.Background(), constFn("v1", new(atomic.Int64)))
		require.NoError(t, err)

		time.Sleep(40 * time.Minute)

		v, err := p.Do(context.Background(), constFn("v2", new(atomic.Int64)))
		require.NoError(t, err)
		require.Equal(t, "v1", v, "served from the cache while the reload runs")

		synctest.Wait()
		assert.Equal(t, int64(1), counters.refreshed.Load())

		v, err = p.Do(context.Background(), failIfCalled(t))
		require.NoError(t, err)
		assert.Equal(t, "v2", v)
	})
}

// A caller still waiting on a failed coalesced call gets the downstream error,
// and nothing is stored.
func TestWithCacheCoalesceWaitingCallerGetsError(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var counters cacheCounters

		p := newCachedCoalescedPolicy(&counters)

		v, err := p.Do(context.Background(), func(context.Context) (string, error) {
			time.Sleep(time.Second)

			return "", errDownstream
		})
		require.ErrorIs(t, err, errDownstream)
		assert.Empty(t, v)
		assert.Zero(t, counters.stores.Load())
	})
}

// The function passed to Do never sees the cache write of its miss, whether
// the call is coalesced or opts out of coalescing.
func TestWithCacheCoalesceFunctionSeesNoCacheWrite(t *testing.T) {
	t.Parallel()

	for _, coalesceKey := range []string{"c", ""} {
		t.Run("coalesce key "+coalesceKey, func(t *testing.T) {
			t.Parallel()

			p := NewPolicy[string](
				"",
				WithCache(newMemCache[CacheEntry[string]](), cacheKeyOf, time.Hour),
				WithCoalesce(coalesceKeyOf),
				WithTimeout(10*time.Second),
			)

			_, err := p.Do(withKeys(context.Background(), "a", coalesceKey), func(ctx context.Context) (string, error) {
				assert.Nil(t, ctx.Value(pendingStoreKey{}))

				return "v", nil
			})
			require.NoError(t, err)
		})
	}
}

// Coalescing changes who stores a miss's result, not what is stored: one
// success and one failure give the same stores with and without it.
func TestWithCacheStoresTheSameWithAndWithoutCoalesce(t *testing.T) {
	t.Parallel()

	for _, coalesce := range []bool{false, true} {
		t.Run(fmt.Sprint("coalesce=", coalesce), func(t *testing.T) {
			t.Parallel()

			var counters cacheCounters

			opts := []Option{
				WithCache(newMemCache[CacheEntry[string]](), cacheKeyOf, time.Hour),
				WithTimeout(10 * time.Second),
				WithHooks(counters.hooks()),
			}
			if coalesce {
				opts = append(opts, WithCoalesce(cacheKeyOf))
			}

			p := NewPolicy[string]("", opts...)

			_, err := p.Do(withKeys(context.Background(), "ok", ""), constFn("v", new(atomic.Int64)))
			require.NoError(t, err)

			_, err = p.Do(withKeys(context.Background(), "ko", ""), errFn(errDownstream, new(atomic.Int64)))
			require.ErrorIs(t, err, errDownstream)

			assert.Equal(t, int64(1), counters.stores.Load())
		})
	}
}

// takePending only takes a write its own cache handed down. A write from
// another cache stays in the context, untouched.
func TestReadThroughTakePendingIgnoresAnotherCache(t *testing.T) {
	t.Parallel()

	own := NewReadThroughCache[string](newMemCache[CacheEntry[string]](), time.Hour, cacheHandOff())
	other := NewReadThroughCache[string](newMemCache[CacheEntry[string]](), time.Hour, cacheHandOff())

	foreign := &pendingStore[string]{rc: other, key: "k"}
	ctx := context.WithValue(context.Background(), pendingStoreKey{}, foreign)

	pending, rest := own.takePending(ctx)
	assert.Nil(t, pending)
	assert.Same(t, foreign, rest.Value(pendingStoreKey{}))

	pending, rest = other.takePending(ctx)
	assert.Same(t, foreign, pending)
	assert.Nil(t, rest.Value(pendingStoreKey{}))
}
