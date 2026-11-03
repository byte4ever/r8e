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

// bulkheadCounters counts the bulkhead events one set of hooks receives.
type bulkheadCounters struct {
	acquired, released, full, queued atomic.Int64
}

func (c *bulkheadCounters) hooks() *Hooks {
	return &Hooks{
		OnBulkheadAcquired: func() { c.acquired.Add(1) },
		OnBulkheadReleased: func() { c.released.Add(1) },
		OnBulkheadFull:     func() { c.full.Add(1) },
		OnBulkheadQueued:   func() { c.queued.Add(1) },
	}
}

// Two policies sharing one bulkhead share its ceiling. Each reports its own
// calls to its own hooks, and both show the shared state in their metrics and
// health. The hooks the bulkhead was built with receive nothing.
func TestWithSharedBulkheadSharesTheCeiling(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var own, routeA, routeB bulkheadCounters

		shared := NewBulkhead(1, RealClock{}, own.hooks())

		a := NewPolicy[string]("route-a", WithRegistry(NewRegistry()),
			WithSharedBulkhead(shared), WithHooks(routeA.hooks()))
		b := NewPolicy[string]("route-b", WithRegistry(NewRegistry()),
			WithSharedBulkhead(shared), WithHooks(routeB.hooks()))

		release := make(chan struct{})
		done := make(chan error, 1)

		go func() {
			_, err := a.Do(t.Context(), func(context.Context) (string, error) {
				<-release

				return "a", nil
			})
			done <- err
		}()

		synctest.Wait()

		_, err := b.Do(t.Context(), func(context.Context) (string, error) { return "b", nil })
		require.ErrorIs(t, err, ErrBulkheadFull, "route A holds the only slot")

		for _, p := range []*Policy[string]{a, b} {
			m := p.Metrics()
			assert.Equal(t, int64(1), m.BulkheadInUse, p.Name())
			assert.Equal(t, int64(1), m.BulkheadCap, p.Name())
			assert.Contains(t, p.HealthStatus().Conditions, ConditionBulkheadFull, p.Name())
		}

		close(release)
		require.NoError(t, <-done)

		assert.Equal(t, int64(1), routeA.acquired.Load())
		assert.Equal(t, int64(1), routeA.released.Load())
		assert.Zero(t, routeA.full.Load())
		assert.Equal(t, int64(1), routeB.full.Load())
		assert.Zero(t, routeB.acquired.Load())
		assert.Zero(t, own.acquired.Load()+own.released.Load()+own.full.Load(),
			"the bulkhead's own hooks see no policy call")
		assert.Equal(t, int64(1), b.Metrics().BulkheadRejected)
		assert.Zero(t, a.Metrics().BulkheadRejected)
	})
}

// A queued caller of one policy is granted the slot another policy releases,
// and the grant is reported to the waiting policy.
func TestWithSharedBulkheadHandsTheSlotAcrossPolicies(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var routeA, routeB bulkheadCounters

		shared := NewBulkhead(1, RealClock{}, &Hooks{}, BulkheadMaxWait(time.Second))

		a := NewPolicy[string]("", WithSharedBulkhead(shared), WithHooks(routeA.hooks()))
		b := NewPolicy[string]("", WithSharedBulkhead(shared), WithHooks(routeB.hooks()))

		aDone := make(chan error, 1)

		go func() {
			_, err := a.Do(t.Context(), func(context.Context) (string, error) {
				time.Sleep(100 * time.Millisecond)

				return "a", nil
			})
			aDone <- err
		}()

		synctest.Wait()

		v, err := b.Do(t.Context(), func(context.Context) (string, error) { return "b", nil })
		require.NoError(t, err)
		assert.Equal(t, "b", v)

		// Route A reports its release after handing the slot over: wait for
		// its call to return before reading its counter.
		require.NoError(t, <-aDone)

		assert.Equal(t, int64(1), routeB.queued.Load(), "route B queued")
		assert.Equal(t, int64(1), routeB.acquired.Load(), "route B was granted the slot")
		assert.Equal(t, int64(1), routeA.released.Load(), "route A released the slot")
	})
}

// The slot covers the whole call, retries and backoff included, as with
// WithBulkhead.
func TestWithSharedBulkheadHoldsTheSlotAcrossRetries(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		shared := NewBulkhead(1, RealClock{}, &Hooks{})

		retrying := NewPolicy[string]("", WithSharedBulkhead(shared),
			WithRetry(2, ConstantBackoff(time.Second)))
		other := NewPolicy[string]("", WithSharedBulkhead(shared))

		var attempts atomic.Int64

		go func() {
			_, _ = retrying.Do(t.Context(), func(context.Context) (string, error) {
				if attempts.Add(1) == 1 {
					return "", errors.New("first attempt fails")
				}

				return "ok", nil
			})
		}()

		// The first attempt failed and the retry sleeps its backoff.
		time.Sleep(500 * time.Millisecond)

		_, err := other.Do(t.Context(), func(context.Context) (string, error) { return "", nil })
		require.ErrorIs(t, err, ErrBulkheadFull, "the slot is held during the backoff")

		// Let the backoff end and the second attempt run.
		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, int64(2), attempts.Load())
	})
}

// A shared bulkhead cannot sit next to a policy's own concurrency limiter.
func TestWithSharedBulkheadConflicts(t *testing.T) {
	t.Parallel()

	tests := map[string][]Option{
		"with WithBulkhead":            {WithBulkhead(4)},
		"with WithAdaptiveConcurrency": {WithAdaptiveConcurrency()},
		"with both":                    {WithBulkhead(4), WithAdaptiveConcurrency()},
	}

	for name, others := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			shared := NewBulkhead(1, RealClock{}, &Hooks{})
			opts := append([]Option{WithSharedBulkhead(shared)}, others...)

			assert.PanicsWithValue(t, ErrSharedBulkheadConflict, func() {
				NewPolicy[string]("", opts...)
			})
		})
	}
}

// A policy reload never resizes a shared bulkhead: only Bulkhead.Reconfigure
// does, for every policy at once.
func TestWithSharedBulkheadIsNotReconfiguredByAPolicy(t *testing.T) {
	t.Parallel()

	slots := 8
	maxWait := "1s"
	queueDepth := 4
	codelTarget := "5ms"

	tests := map[string]PolicyConfig{
		"size":         {Bulkhead: &slots},
		"max wait":     {BulkheadMaxWait: &maxWait},
		"queue depth":  {BulkheadQueueDepth: &queueDepth},
		"CoDel target": {BulkheadCoDelTarget: &codelTarget},
	}

	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			shared := NewBulkhead(1, RealClock{}, &Hooks{})
			p := NewPolicy[string]("", WithSharedBulkhead(shared))

			err := p.Reconfigure(cfg)

			require.ErrorIs(t, err, ErrSharedBulkheadConflict)
			assert.Equal(t, int64(1), shared.Cap())

			shared.Reconfigure(3)
			assert.Equal(t, int64(3), p.Metrics().BulkheadCap, "the owner resizes it for every policy")
		})
	}
}

// A reload that leaves the bulkhead alone goes through on a policy with a
// shared bulkhead.
func TestWithSharedBulkheadLetsOtherReloadsThrough(t *testing.T) {
	t.Parallel()

	shared := NewBulkhead(1, RealClock{}, &Hooks{})
	p := NewPolicy[string]("", WithSharedBulkhead(shared), WithTimeout(time.Second))

	timeout := "2s"

	require.NoError(t, p.Reconfigure(PolicyConfig{Timeout: &timeout}))
	assert.Equal(t, int64(1), shared.Cap())
}

// A nil shared bulkhead is ignored: it neither attaches a bulkhead nor removes
// one attached before it.
func TestWithSharedBulkheadIgnoresNil(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		opts    func() []Option
		wantCap int64
	}{
		"alone": {
			opts:    func() []Option { return []Option{WithSharedBulkhead(nil)} },
			wantCap: 0,
		},
		"after a shared bulkhead": {
			opts: func() []Option {
				return []Option{WithSharedBulkhead(NewBulkhead(1, RealClock{}, &Hooks{})), WithSharedBulkhead(nil)}
			},
			wantCap: 1,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			p := NewPolicy[string]("", tt.opts()...)

			v, err := p.Do(t.Context(), func(context.Context) (string, error) { return "ok", nil })
			require.NoError(t, err)
			assert.Equal(t, "ok", v)
			assert.Equal(t, tt.wantCap, p.Metrics().BulkheadCap)
		})
	}
}
