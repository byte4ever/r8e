package r8e

import (
	"context"
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
