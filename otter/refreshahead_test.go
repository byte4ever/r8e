package otter

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
)

// raClock is an advanceable r8e.Clock so the refresh-ahead test can age an entry
// into the refresh window deterministically instead of sleeping. It is safe for
// concurrent use: the detached reload goroutine reads Now() (via the cache's
// store) while foreground reads and the test advance it.
type raClock struct {
	mu  sync.Mutex
	now time.Time
}

func newRAClock() *raClock {
	return &raClock{now: time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)}
}

func (c *raClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *raClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

// NewTimer is not on the WithCache+WithTimeout path (the timeout uses a real
// context deadline), but is implemented to fire immediately so any incidental
// use cannot hang the test.
func (c *raClock) NewTimer(time.Duration) r8e.Timer {
	ch := make(chan time.Time, 1)
	ch <- c.Now()

	return raTimer{ch: ch}
}

func (c *raClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

type raTimer struct{ ch chan time.Time }

func (t raTimer) C() <-chan time.Time      { return t.ch }
func (t raTimer) Stop() bool               { return true }
func (t raTimer) Reset(time.Duration) bool { return false }

type raKey struct{}

func withRAKey(ctx context.Context, k string) context.Context {
	return context.WithValue(ctx, raKey{}, k)
}

func raKeyFromCtx(ctx context.Context) string {
	if k, ok := ctx.Value(raKey{}).(string); ok {
		return k
	}

	return ""
}

// TestRefreshAheadWithRealOtterCache drives r8e's refresh-ahead policy against a
// real otter backing cache and proves: (1) an entry aged into the refresh window
// is served immediately while a single deduplicated detached reload repopulates
// it, and because otter applies writes synchronously the refreshed value is
// visible to the very next read; (2) under a concurrent read burst the reload's
// otter.Set races safely against foreground otter.Get (run with -race).
func TestRefreshAheadWithRealOtterCache(t *testing.T) {
	t.Parallel()

	const (
		freshTTL   = time.Minute
		refreshTTL = 30 * time.Second
		readers    = 8
		pollFor    = 2 * time.Second
		pollTick   = 2 * time.Millisecond
	)

	cache := MustNew[string, r8e.CacheEntry[string]](newTestConfig())
	clk := newRAClock()

	var version atomic.Int64

	fetch := func(context.Context) (string, error) {
		return fmt.Sprintf("v%d", version.Add(1)), nil
	}

	policy := r8e.NewPolicy[string]("otter-refresh-ahead",
		r8e.WithCache(cache, raKeyFromCtx, freshTTL, r8e.RefreshAhead(refreshTTL)),
		r8e.WithTimeout(10*time.Second),
		r8e.WithClock(clk),
	)

	ctx := withRAKey(context.Background(), "doc:1")

	// --- Deterministic core: one triggering read, immediate visibility ---

	// Cold read: a miss populates otter with v1.
	got, err := policy.Do(ctx, fetch)
	require.NoError(t, err)
	require.Equal(t, "v1", got)

	// Age into the refresh-ahead window [30s, 60s): still fresh, past threshold.
	clk.advance(45 * time.Second)

	// A single in-window read is served the current value and triggers exactly
	// one background reload (no concurrent readers ⇒ no re-trigger).
	got, err = policy.Do(ctx, fetch)
	require.NoError(t, err)
	require.Equal(t, "v1", got, "the read serves the current value, not the reloaded one")

	// Wait for the detached reload to repopulate otter (metric increments after
	// the successful store).
	require.Eventually(t, func() bool {
		return policy.Metrics().CacheRefreshes >= 1
	}, pollFor, pollTick, "the background reload never repopulated otter")

	require.Equal(t, int64(1), policy.Metrics().CacheRefreshes, "exactly one reload")
	require.Equal(t, int64(2), version.Load(), "miss + one reload")

	// otter exposes writes synchronously, so the refreshed value is a fresh hit
	// on the very next read — no polling needed.
	got, err = policy.Do(ctx, fetch)
	require.NoError(t, err)
	require.Equal(t, "v2", got, "otter serves the refreshed value immediately")
	require.Equal(t, int64(2), version.Load(), "the post-refresh read was a hit, no new fetch")

	// --- Concurrency stress (run under -race): the reload's Set vs foreground Get ---

	// Re-age the refreshed v2 entry back into the refresh window.
	clk.advance(40 * time.Second)

	var wg sync.WaitGroup

	for range readers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			v, doErr := policy.Do(ctx, fetch)
			assert.NoError(t, doErr)
			assert.NotEmpty(t, v) // always a consistent cached value, never blocked
		}()
	}

	wg.Wait()

	// At least one further reload ran concurrently with the foreground reads
	// (the per-key dedup may admit a few via the classify→claim race — benign).
	require.Eventually(t, func() bool {
		return policy.Metrics().CacheRefreshes >= 2
	}, pollFor, pollTick, "the concurrent burst triggered no further reload")
}
