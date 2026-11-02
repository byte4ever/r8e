package r8e

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errCallerGaveUp is the cause a caller gives when it cancels its context.
var errCallerGaveUp = errors.New("caller gave up")

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

// newNegativeCachePolicy builds a policy whose cache records failures for a
// minute, keyed on one constant key, with any extra options.
func newNegativeCachePolicy(opts ...Option) *Policy[string] {
	keyFn := func(context.Context) string { return "k" }

	return NewPolicy[string](
		"",
		append([]Option{
			WithCache(newMemCache[CacheEntry[string]](), keyFn, time.Hour, NegativeCache(time.Minute)),
		}, opts...)...,
	)
}

// A call whose own context ends while it runs leaves no negative entry: the
// downstream did not fail, so the next call goes downstream again.
func TestNegativeCacheIgnoresEndedCallerContext(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		ctx     func() (context.Context, context.CancelFunc)
		wantErr error
	}{
		"deadline": {
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), time.Second)
			},
			wantErr: context.DeadlineExceeded,
		},
		"cancel with a cause": {
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancelCause(context.Background())
				stop := time.AfterFunc(time.Second, func() { cancel(errCallerGaveUp) })

				return ctx, func() { stop.Stop(); cancel(nil) }
			},
			wantErr: context.Canceled,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int64

				p := newNegativeCachePolicy(WithTimeout(10 * time.Second))

				ctx, cancel := tt.ctx()
				defer cancel()

				_, err := p.Do(ctx, slowFn(2*time.Second, "slow", &calls))
				require.ErrorIs(t, err, tt.wantErr)

				v, err := p.Do(context.Background(), constFn("v", &calls))
				require.NoError(t, err)
				assert.Equal(t, "v", v)
				assert.Equal(t, int64(2), calls.Load(), "the second call went downstream")
			})
		})
	}
}

// A downstream that reports its context's end in an error of its own (a gRPC
// status, a %v wrap) is not cached either: the state of the context decides,
// not the identity of the error.
func TestNegativeCacheIgnoresOwnErrorAfterCallerDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64

		p := newNegativeCachePolicy() // no WithTimeout: the error reaches the cache as is

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		_, err := p.Do(ctx, func(ctx context.Context) (string, error) {
			calls.Add(1)
			<-ctx.Done()

			return "", fmt.Errorf("rpc error: code = DeadlineExceeded desc = %v", ctx.Err())
		})
		require.Error(t, err)
		require.NotErrorIs(t, err, context.DeadlineExceeded, "the error does not wrap the context's")

		v, err := p.Do(context.Background(), constFn("v", &calls))
		require.NoError(t, err)
		assert.Equal(t, "v", v)
		assert.Equal(t, int64(2), calls.Load(), "the second call went downstream")
	})
}

// An enclosing policy that ends the call with its own deadline does not leave
// a negative entry in the inner cache: its callers may carry no such deadline.
func TestNegativeCacheIgnoresEnclosingPolicyDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int64

		inner := newNegativeCachePolicy(WithTimeout(10 * time.Second))
		outer := NewPolicy[string]("", WithTimeout(time.Second))

		_, err := outer.Do(context.Background(), func(ctx context.Context) (string, error) {
			return inner.Do(ctx, slowFn(2*time.Second, "slow", &calls))
		})
		require.ErrorIs(t, err, ErrTimeout)

		time.Sleep(2 * time.Second)
		synctest.Wait()

		v, err := inner.Do(context.Background(), constFn("v", &calls))
		require.NoError(t, err)
		assert.Equal(t, "v", v)
		assert.Equal(t, int64(2), calls.Load(), "the second call went downstream")
	})
}

// A downstream failure on a call whose context is still alive is cached, and
// so is the policy's own timeout.
func TestNegativeCacheStillRecordsDownstreamFailure(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		fn      func(*atomic.Int64) func(context.Context) (string, error)
		wantErr error
	}{
		"downstream error": {
			fn: func(calls *atomic.Int64) func(context.Context) (string, error) {
				return errFn(errDownstream, calls)
			},
			wantErr: errDownstream,
		},
		"policy timeout": {
			fn: func(calls *atomic.Int64) func(context.Context) (string, error) {
				return slowFn(2*time.Second, "slow", calls)
			},
			wantErr: ErrTimeout,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var calls atomic.Int64

				p := newNegativeCachePolicy(WithTimeout(time.Second))

				_, err := p.Do(context.Background(), tt.fn(&calls))
				require.ErrorIs(t, err, tt.wantErr)

				// Served from the negative entry: a call downstream would
				// succeed with "v".
				v, err := p.Do(context.Background(), constFn("v", &calls))
				require.ErrorIs(t, err, tt.wantErr)
				assert.Empty(t, v)
				assert.Equal(t, int64(1), calls.Load())
			})
		})
	}
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
