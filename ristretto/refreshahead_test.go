package ristretto

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

// TestRefreshAheadWithRealRistrettoCache drives r8e's refresh-ahead policy
// against a real ristretto backing cache. ristretto admits writes asynchronously
// (and may drop them under buffer pressure), so a refresh-ahead reload becomes
// visible EVENTUALLY rather than immediately — exactly the nuance the read-through
// cache already lives with for every Set. The test proves: (1) the detached
// reload runs against the real cache (its loader re-executes, CacheRefreshes
// increments) and the refreshed value eventually supersedes the stale one; (2)
// under a concurrent read burst the reload's SetWithTTL races safely against
// foreground Get (run with -race).
func TestRefreshAheadWithRealRistrettoCache(t *testing.T) {
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

	policy := r8e.NewPolicy[string]("ristretto-refresh-ahead",
		r8e.WithCache(cache, raKeyFromCtx, freshTTL, r8e.RefreshAhead(refreshTTL)),
		r8e.WithTimeout(10*time.Second),
		r8e.WithClock(clk),
	)

	ctx := withRAKey(context.Background(), "doc:1")

	// --- Functional core: the reload runs against ristretto, visible eventually ---

	// Cold read: a miss runs the loader (v1) and stores it; ristretto returns the
	// value to the caller directly while it admits the write asynchronously.
	got, err := policy.Do(ctx, fetch)
	require.NoError(t, err)
	require.Equal(t, "v1", got)

	// Wait for the cold write to actually land by polling the BACKING cache
	// directly (CacheEntry stays opaque — only presence is inspected), so we do
	// NOT re-enter Do and accidentally re-fetch on a still-empty cache.
	require.Eventually(t, func() bool {
		_, ok := cache.Get("doc:1")

		return ok
	}, pollFor, pollTick, "cold value never admitted to ristretto")

	// Age into the refresh-ahead window [30s, 60s): still fresh, past threshold.
	clk.advance(45 * time.Second)

	// An in-window read is served the (admitted) current value and triggers a
	// background reload against ristretto.
	got, err = policy.Do(ctx, fetch)
	require.NoError(t, err)
	require.Equal(t, "v1", got, "the read serves the current value, not the reloaded one")

	// The detached reload ran against the real ristretto cache: its loader
	// re-executed and the refresh metric incremented.
	require.Eventually(t, func() bool {
		return policy.Metrics().CacheRefreshes >= 1
	}, pollFor, pollTick, "no refresh-ahead reload ran against ristretto")
	require.GreaterOrEqual(t, version.Load(), int64(2), "the reload re-ran the loader")

	// Eventually the refreshed value supersedes the stale one once ristretto
	// applies the buffered write. Reads in the meantime keep being served the
	// previous value (never a synchronous miss while it is still fresh); each such
	// read may re-trigger a reload, which is the documented eventual-consistency
	// behaviour, not a defect — hence the >= assertions above.
	require.Eventually(t, func() bool {
		v, doErr := policy.Do(ctx, fetch)

		return doErr == nil && v != "v1"
	}, pollFor, pollTick, "refreshed value never became visible in ristretto")

	// --- Concurrency stress (run under -race): the reload's Set vs foreground Get ---

	clk.advance(45 * time.Second) // re-age the now-current entry into the window

	var wg sync.WaitGroup

	for range readers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			v, doErr := policy.Do(ctx, fetch)
			assert.NoError(t, doErr)
			assert.NotEmpty(t, v) // a consistent cached value, never blocked
		}()
	}

	wg.Wait()
}
