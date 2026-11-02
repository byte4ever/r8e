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

// slowNegativeCache delays every write of a negative entry, to reproduce a
// caller's negative write landing after the coalesced call's success.
type slowNegativeCache struct {
	*memCache[CacheEntry[string]]
	delay time.Duration
}

func (c slowNegativeCache) Set(key string, value CacheEntry[string], ttl time.Duration) {
	if value.err != nil {
		time.Sleep(c.delay)
	}

	c.memCache.Set(key, value, ttl)
}

// A caller whose own deadline ends the call leaves no negative entry: the
// downstream did not fail, so the next call goes downstream again.
func TestNegativeCacheIgnoresCallerDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64

		keyFn := func(context.Context) string { return "k" }
		p := NewPolicy[string](
			"",
			WithCache(newMemCache[CacheEntry[string]](), keyFn, time.Hour, NegativeCache(time.Minute)),
			WithTimeout(10*time.Second),
		)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := p.Do(ctx, slowFn(2*time.Second, "slow", &calls))
		require.ErrorIs(t, err, context.DeadlineExceeded)

		v, err := p.Do(context.Background(), constFn("v", &calls))
		require.NoError(t, err)
		assert.Equal(t, "v", v)
		assert.Equal(t, int64(2), calls.Load(), "the second call went downstream")
	})
}

// A caller cancelled by its own context leaves no negative entry either.
func TestNegativeCacheIgnoresCallerCancel(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64

		keyFn := func(context.Context) string { return "k" }
		p := NewPolicy[string](
			"",
			WithCache(newMemCache[CacheEntry[string]](), keyFn, time.Hour, NegativeCache(time.Minute)),
			WithTimeout(10*time.Second),
		)

		ctx, cancel := context.WithCancelCause(context.Background())
		time.AfterFunc(time.Second, func() { cancel(errDownstream) })

		_, err := p.Do(ctx, slowFn(2*time.Second, "slow", &calls))
		require.Error(t, err)

		v, err := p.Do(context.Background(), constFn("v", &calls))
		require.NoError(t, err)
		assert.Equal(t, "v", v)
	})
}

// A downstream failure is still cached negatively.
func TestNegativeCacheStillRecordsDownstreamFailure(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64

	keyFn := func(context.Context) string { return "k" }
	p := NewPolicy[string](
		"",
		WithCache(newMemCache[CacheEntry[string]](), keyFn, time.Hour, NegativeCache(time.Minute)),
		WithTimeout(10*time.Second),
	)

	_, err := p.Do(context.Background(), errFn(errDownstream, &calls))
	require.ErrorIs(t, err, errDownstream)

	_, err = p.Do(context.Background(), failIfCalled(t))
	require.ErrorIs(t, err, errDownstream, "served from the negative entry")
	assert.Equal(t, int64(1), calls.Load())
}

// With coalescing, the leader's deadline can expire while the coalesced call
// stores its success. The leader must not overwrite that success with a
// negative entry for its own deadline.
func TestNegativeCacheKeepsCoalescedSuccessAfterLeaderDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64

		keyFn := func(context.Context) string { return "k" }
		cache := slowNegativeCache{memCache: newMemCache[CacheEntry[string]](), delay: 2 * time.Second}
		p := NewPolicy[string](
			"",
			WithCache[string](cache, keyFn, time.Hour, NegativeCache(time.Hour)),
			WithCoalesce(keyFn),
			WithTimeout(10*time.Second),
		)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := p.Do(ctx, slowFn(2*time.Second, "v", &calls))
		require.ErrorIs(t, err, context.DeadlineExceeded)

		time.Sleep(5 * time.Second)
		synctest.Wait()

		v, err := p.Do(context.Background(), failIfCalled(t))
		require.NoError(t, err)
		assert.Equal(t, "v", v)
	})
}
