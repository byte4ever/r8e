package r8e

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// memCache is a minimal in-memory Cache for read-through tests. It ignores the
// per-entry TTL — freshness in [ReadThroughCache] is decided by the injected
// Clock, not the backing cache's own expiry — so the tests drive aging through
// the clock and never depend on real time.
type memCache[V any] struct {
	mu   sync.Mutex
	data map[string]V
	ttls map[string]time.Duration // last TTL passed to Set, per key (spy)
	sets atomic.Int64
}

func newMemCache[V any]() *memCache[V] {
	return &memCache[V]{
		data: make(map[string]V),
		ttls: make(map[string]time.Duration),
	}
}

//nolint:ireturn // generic value type V, not an interface
func (c *memCache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	v, ok := c.data[key]

	return v, ok
}

func (c *memCache[V]) Set(key string, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.sets.Add(1)
	c.data[key] = value
	c.ttls[key] = ttl
}

func (c *memCache[V]) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.data, key)
	delete(c.ttls, key)
}

// lastTTL returns the TTL the most recent Set used for key (spy accessor).
func (c *memCache[V]) lastTTL(key string) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.ttls[key]
}

// cacheCounters tallies the read-through hooks so tests can assert which events
// fired without caring about ordering.
type cacheCounters struct {
	hits, misses, stores, stale, refreshed atomic.Int64
}

func (c *cacheCounters) hooks() *Hooks {
	return &Hooks{
		OnCacheHit:       func() { c.hits.Add(1) },
		OnCacheMiss:      func() { c.misses.Add(1) },
		OnCacheStored:    func() { c.stores.Add(1) },
		OnStaleServed:    func() { c.stale.Add(1) },
		OnCacheRefreshed: func() { c.refreshed.Add(1) },
	}
}

// constFn returns a next function that always succeeds with val and counts
// invocations.
func constFn(val string, calls *atomic.Int64) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		calls.Add(1)

		return val, nil
	}
}

// errFn returns a next function that always fails with err and counts
// invocations.
func errFn(err error, calls *atomic.Int64) func(context.Context) (string, error) {
	return func(context.Context) (string, error) {
		calls.Add(1)

		return "", err
	}
}

const cacheTTL = time.Minute

var errDownstream = errors.New("downstream failed")

// newStringRTC builds a string-valued ReadThroughCache with the fixed test TTL,
// injecting clk and hooks through the CacheClock/CacheHooks options exactly as
// the policy layer does.
func newStringRTC(
	cache Cache[string, CacheEntry[string]],
	clk Clock,
	hooks *Hooks,
	opts ...CacheOption,
) *ReadThroughCache[string] {
	all := append([]CacheOption{CacheClock(clk), CacheHooks(hooks)}, opts...)

	return NewReadThroughCache[string](cache, cacheTTL, all...)
}

// ---------------------------------------------------------------------------
// ReadThroughCache.Do — read-through (fresh hit / miss)
// ---------------------------------------------------------------------------

func TestReadThroughEmptyKeyIsPassThrough(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	rc := newStringRTC(cache, newPolicyClock(), ctr.hooks())

	var calls atomic.Int64

	got, err := rc.Do(context.Background(), "", constFn("v", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v", got)

	assert.Equal(t, int64(1), calls.Load(), "next must run for an empty key")
	assert.Equal(t, int64(0), cache.sets.Load(), "empty key must not be cached")
	assert.Equal(t, int64(0), ctr.hits.Load()+ctr.misses.Load(),
		"empty key bypasses hit/miss accounting")
}

func TestReadThroughMissThenFreshHit(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	rc := newStringRTC(cache, newPolicyClock(), ctr.hooks())

	var calls atomic.Int64

	// First call: miss, executes, caches.
	got, err := rc.Do(context.Background(), "k", constFn("v1", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", got)

	// Second call: fresh hit, next must not run again.
	got, err = rc.Do(context.Background(), "k", constFn("v2", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", got, "fresh hit returns the cached value, not v2")

	assert.Equal(t, int64(1), calls.Load(), "next runs once across miss + hit")
	assert.Equal(t, int64(1), ctr.misses.Load())
	assert.Equal(t, int64(1), ctr.hits.Load())
	assert.Equal(t, int64(1), ctr.stores.Load())
}

func TestReadThroughForceRefreshBypassesRead(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	rc := newStringRTC(cache, newPolicyClock(), ctr.hooks())

	var calls atomic.Int64

	_, err := rc.Do(context.Background(), "k", constFn("v1", &calls))
	require.NoError(t, err)

	// Force-refresh skips the fresh hit and re-executes, repopulating the cache.
	got, err := rc.Do(ForceRefresh(context.Background()), "k", constFn("v2", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v2", got)

	// A subsequent normal call sees the refreshed value as a fresh hit.
	got, err = rc.Do(context.Background(), "k", constFn("v3", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v2", got)

	assert.Equal(t, int64(2), calls.Load(), "miss + force-refresh execute; final is a hit")
	assert.Equal(t, int64(2), ctr.misses.Load(), "force-refresh counts as a miss")
	assert.Equal(t, int64(1), ctr.hits.Load())
}

// ---------------------------------------------------------------------------
// ReadThroughCache.Do — stale-if-error
// ---------------------------------------------------------------------------

func TestReadThroughStaleServedOnError(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	clk := newPolicyClock()
	rc := newStringRTC(cache, clk, ctr.hooks(), StaleIfError(time.Hour))

	var calls atomic.Int64

	_, err := rc.Do(context.Background(), "k", constFn("good", &calls))
	require.NoError(t, err)

	// Age past the fresh TTL but within the stale window.
	clk.advance(cacheTTL + time.Minute)

	// Revalidation fails -> serve the stale value, masking the error.
	got, err := rc.Do(context.Background(), "k", errFn(errDownstream, &calls))
	require.NoError(t, err)
	assert.Equal(t, "good", got, "stale value served on revalidation failure")

	assert.Equal(t, int64(2), calls.Load(), "stale revalidation executes next")
	assert.Equal(t, int64(2), ctr.misses.Load(), "stale revalidation counts as a miss")
	assert.Equal(t, int64(1), ctr.stale.Load())
}

func TestReadThroughStaleRevalidationSuccessRefreshes(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	clk := newPolicyClock()
	rc := newStringRTC(cache, clk, ctr.hooks(), StaleIfError(time.Hour))

	var calls atomic.Int64

	_, err := rc.Do(context.Background(), "k", constFn("old", &calls))
	require.NoError(t, err)

	clk.advance(cacheTTL + time.Minute) // now stale

	got, err := rc.Do(context.Background(), "k", constFn("new", &calls))
	require.NoError(t, err)
	assert.Equal(t, "new", got, "successful revalidation returns the fresh value")

	// The refreshed value is now a fresh hit.
	got, err = rc.Do(context.Background(), "k", constFn("unused", &calls))
	require.NoError(t, err)
	assert.Equal(t, "new", got)

	assert.Equal(t, int64(2), calls.Load())
	assert.Equal(t, int64(0), ctr.stale.Load(), "no stale serve on a successful revalidation")
	assert.Equal(t, int64(2), ctr.stores.Load())
}

func TestReadThroughExpiredBeyondStaleIsMiss(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	clk := newPolicyClock()
	rc := newStringRTC(cache, clk, ctr.hooks(), StaleIfError(time.Minute))

	var calls atomic.Int64

	_, err := rc.Do(context.Background(), "k", constFn("good", &calls))
	require.NoError(t, err)

	// Age past fresh+stale: the entry is logically gone even though memCache
	// still holds it, so an error must propagate (no stale to serve).
	clk.advance(cacheTTL + time.Minute + time.Second)

	_, err = rc.Do(context.Background(), "k", errFn(errDownstream, &calls))
	require.ErrorIs(t, err, errDownstream)
	assert.Equal(t, int64(0), ctr.stale.Load(), "expired entry is not served as stale")
}

// ---------------------------------------------------------------------------
// ReadThroughCache.Do — negative caching
// ---------------------------------------------------------------------------

func TestReadThroughNegativeCacheFastFails(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	clk := newPolicyClock()
	rc := newStringRTC(cache, clk, ctr.hooks(), NegativeCache(time.Minute))

	var calls atomic.Int64

	// First failure is cached as a negative entry.
	_, err := rc.Do(context.Background(), "k", errFn(errDownstream, &calls))
	require.ErrorIs(t, err, errDownstream)

	// Second call fast-fails from the negative entry without executing.
	_, err = rc.Do(context.Background(), "k", errFn(errors.New("unused"), &calls))
	require.ErrorIs(t, err, errDownstream, "negative hit replays the recorded error")

	assert.Equal(t, int64(1), calls.Load(), "negative hit must not execute next")
	assert.Equal(t, int64(1), ctr.hits.Load(), "negative hit counts as a hit")
	assert.Equal(t, int64(1), ctr.misses.Load())

	// After the negative TTL passes, the key executes again.
	clk.advance(2 * time.Minute)

	_, err = rc.Do(context.Background(), "k", errFn(errDownstream, &calls))
	require.ErrorIs(t, err, errDownstream)
	assert.Equal(t, int64(2), calls.Load(), "expired negative entry re-executes")
}

func TestReadThroughNegativeDisabledDoesNotCacheErrors(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	rc := newStringRTC(cache, newPolicyClock(), ctr.hooks())

	var calls atomic.Int64

	for range 3 {
		_, err := rc.Do(context.Background(), "k", errFn(errDownstream, &calls))
		require.ErrorIs(t, err, errDownstream)
	}

	assert.Equal(t, int64(3), calls.Load(), "every call executes without negative caching")
	assert.Equal(t, int64(0), cache.sets.Load(), "failures are not cached")
}

func TestReadThroughNegativeStoreDoesNotFireOnCacheStored(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	rc := newStringRTC(cache, newPolicyClock(), ctr.hooks(), NegativeCache(time.Minute))

	var calls atomic.Int64

	_, err := rc.Do(context.Background(), "k", errFn(errDownstream, &calls))
	require.ErrorIs(t, err, errDownstream)

	assert.Equal(t, int64(1), cache.sets.Load(), "a negative entry is written")
	assert.Equal(t, int64(0), ctr.stores.Load(),
		"OnCacheStored fires for successful results only, not negative entries")
}

func TestReadThroughStaleWinsOverNegativeCache(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	clk := newPolicyClock()
	rc := newStringRTC(
		cache, clk, ctr.hooks(), StaleIfError(time.Hour), NegativeCache(time.Minute),
	)

	var calls atomic.Int64

	_, err := rc.Do(context.Background(), "k", constFn("good", &calls))
	require.NoError(t, err)

	clk.advance(cacheTTL + time.Minute) // now stale

	// Revalidation fails: stale must be served AND no negative entry written over it.
	got, err := rc.Do(context.Background(), "k", errFn(errDownstream, &calls))
	require.NoError(t, err)
	assert.Equal(t, "good", got)
	assert.Equal(t, int64(1), ctr.stale.Load())

	// The success entry is preserved (no negative entry overwrote it): a second
	// failing revalidation still serves stale rather than fast-failing.
	got, err = rc.Do(context.Background(), "k", errFn(errors.New("again"), &calls))
	require.NoError(t, err, "stale entry preserved; no negative entry was written over it")
	assert.Equal(t, "good", got)
	assert.Equal(t, int64(2), ctr.stale.Load())
}

func TestReadThroughForceRefreshIgnoresStaleAndNegative(t *testing.T) {
	t.Parallel()

	// (a) Force-refresh past the fresh TTL with a serveable stale entry: a failing
	// revalidation must return the error, NOT the stale value (force-refresh skips
	// the stale read entirely).
	cacheA := newMemCache[CacheEntry[string]]()
	ctrA := &cacheCounters{}
	clkA := newPolicyClock()
	rcA := newStringRTC(cacheA, clkA, ctrA.hooks(), StaleIfError(time.Hour))

	var callsA atomic.Int64

	_, err := rcA.Do(context.Background(), "k", constFn("good", &callsA))
	require.NoError(t, err)

	clkA.advance(cacheTTL + time.Minute) // a normal call here would serve stale on error

	_, err = rcA.Do(ForceRefresh(context.Background()), "k", errFn(errDownstream, &callsA))
	require.ErrorIs(t, err, errDownstream, "force-refresh bypasses the stale fallback")
	assert.Equal(t, int64(0), ctrA.stale.Load(), "no stale serve under force-refresh")

	// (b) Force-refresh with a valid negative entry must re-execute, not fast-fail.
	cacheB := newMemCache[CacheEntry[string]]()
	rcB := newStringRTC(cacheB, newPolicyClock(), &Hooks{}, NegativeCache(time.Hour))

	var callsB atomic.Int64

	_, err = rcB.Do(context.Background(), "k", errFn(errDownstream, &callsB))
	require.ErrorIs(t, err, errDownstream) // negative entry now cached

	got, err := rcB.Do(ForceRefresh(context.Background()), "k", constFn("fresh", &callsB))
	require.NoError(t, err, "force-refresh bypasses the negative entry and re-executes")
	assert.Equal(t, "fresh", got)
	assert.Equal(t, int64(2), callsB.Load())
}

func TestReadThroughCachesZeroValue(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	rc := newStringRTC(cache, newPolicyClock(), ctr.hooks())

	var calls atomic.Int64

	// next returns the zero value ("") with no error — a legitimate success that
	// must be cached, not mistaken for a miss/negative (entries are distinguished
	// by err, not by value).
	got, err := rc.Do(context.Background(), "k", constFn("", &calls))
	require.NoError(t, err)
	assert.Empty(t, got)

	got, err = rc.Do(context.Background(), "k", constFn("later", &calls))
	require.NoError(t, err)
	assert.Empty(t, got, "the cached zero value is served as a fresh hit")
	assert.Equal(t, int64(1), calls.Load(), "zero value is cached; next is not re-run")
	assert.Equal(t, int64(1), ctr.hits.Load())
}

func TestReadThroughStoresWithCorrectTTL(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	rc := newStringRTC(
		cache, newPolicyClock(), &Hooks{},
		StaleIfError(2*time.Minute), NegativeCache(15*time.Second),
	)

	var calls atomic.Int64

	// A success is stored for the full fresh+stale lifetime, so a real TTL-honouring
	// backing cache keeps it available through the stale window.
	_, err := rc.Do(context.Background(), "ok", constFn("v", &calls))
	require.NoError(t, err)
	assert.Equal(t, cacheTTL+2*time.Minute, cache.lastTTL("ok"),
		"success entry stored for the fresh+stale lifetime")

	// A negative entry is stored for the negative TTL only.
	_, err = rc.Do(context.Background(), "bad", errFn(errDownstream, &calls))
	require.ErrorIs(t, err, errDownstream)
	assert.Equal(t, 15*time.Second, cache.lastTTL("bad"),
		"negative entry stored for the negative TTL")
}

// ---------------------------------------------------------------------------
// classify — white-box edge cases
// ---------------------------------------------------------------------------

func TestClassify(t *testing.T) {
	t.Parallel()

	clk := newPolicyClock()
	cache := newMemCache[CacheEntry[string]]()
	// freshTTL = cacheTTL (1m), staleTTL = 1m, negTTL = 30s.
	rc := newStringRTC(cache, clk, &Hooks{}, StaleIfError(time.Minute), NegativeCache(30*time.Second))

	base := clk.Now()
	cache.Set("success", CacheEntry[string]{value: "v", storedAt: base}, 0)
	cache.Set("negative", CacheEntry[string]{err: errDownstream, storedAt: base}, 0)

	// Within all windows: success is a fresh hit, negative is a negative hit.
	entry, state := rc.classify("success")
	assert.Equal(t, entryFresh, state)
	assert.Equal(t, "v", entry.value)

	_, state = rc.classify("negative")
	assert.Equal(t, entryNegativeHit, state)

	// Missing key.
	_, state = rc.classify("missing")
	assert.Equal(t, entryMiss, state)

	// Age past fresh (1m) and past negTTL (30s): success is now stale, negative expired.
	clk.advance(cacheTTL + 30*time.Second)

	_, state = rc.classify("success")
	assert.Equal(t, entryStale, state, "success entry is stale in the stale window")

	_, state = rc.classify("negative")
	assert.Equal(t, entryMiss, state, "negative entry expired beyond its TTL")

	// Age past fresh+stale (2m): success entry now logically gone.
	clk.advance(time.Hour)

	_, state = rc.classify("success")
	assert.Equal(t, entryMiss, state, "success entry expired beyond the stale window")
}

// ---------------------------------------------------------------------------
// NewReadThroughCache — defaults
// ---------------------------------------------------------------------------

func TestNewReadThroughCacheDefaults(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()

	// With no CacheClock/CacheHooks options, and with nil passed to each (which
	// is ignored), the defaults RealClock and the no-op zero Hooks apply.
	for _, rc := range []*ReadThroughCache[string]{
		NewReadThroughCache[string](cache, cacheTTL),
		NewReadThroughCache[string](cache, cacheTTL, CacheClock(nil), CacheHooks(nil)),
	} {
		require.NotNil(t, rc.hooks)
		_, isReal := rc.clock.(RealClock)
		assert.True(t, isReal, "nil clock defaults to RealClock")

		var calls atomic.Int64

		got, err := rc.Do(context.Background(), "k", constFn("v", &calls))
		require.NoError(t, err)
		assert.Equal(t, "v", got)
	}
}

func TestIsForceRefresh(t *testing.T) {
	t.Parallel()

	assert.False(t, isForceRefresh(context.Background()), "absent marker")
	assert.True(t, isForceRefresh(ForceRefresh(context.Background())))

	// A non-bool value under the key is treated as absent.
	ctx := context.WithValue(context.Background(), forceRefreshKey{}, "yes")
	assert.False(t, isForceRefresh(ctx))
}

// ---------------------------------------------------------------------------
// Concurrency — -race
// ---------------------------------------------------------------------------

func TestReadThroughConcurrentDo(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	rc := newStringRTC(cache, newPolicyClock(), &Hooks{}, NegativeCache(time.Second))

	var wg sync.WaitGroup

	for i := range 50 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			key := "k"
			if i%2 == 0 {
				key = "other"
			}

			_, _ = rc.Do(context.Background(), key, func(context.Context) (string, error) {
				return "v", nil
			})
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Policy integration — WithCache
// ---------------------------------------------------------------------------

func TestWithCacheShortCircuitsChain(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()

	var calls atomic.Int64

	p := NewPolicy[string](
		"",
		WithCache(cache, func(context.Context) string { return "k" }, cacheTTL),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
	)

	fn := func(context.Context) (string, error) {
		calls.Add(1)

		return "v", nil
	}

	for range 5 {
		got, err := p.Do(context.Background(), fn)
		require.NoError(t, err)
		assert.Equal(t, "v", got)
	}

	assert.Equal(t, int64(1), calls.Load(), "a fresh hit skips the rest of the chain")
}

func TestWithCacheEmptyKeyDisablesCaching(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()

	var calls atomic.Int64

	// keyFn returns "" -> every call runs the function.
	p := NewPolicy[string](
		"",
		WithCache(cache, func(context.Context) string { return "" }, cacheTTL),
	)

	for range 3 {
		_, err := p.Do(context.Background(), constFn("v", &calls))
		require.NoError(t, err)
	}

	assert.Equal(t, int64(3), calls.Load())
	assert.Equal(t, int64(0), cache.sets.Load())
}

func TestWithCacheForceRefreshThroughPolicy(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()

	var calls atomic.Int64

	p := NewPolicy[string](
		"",
		WithCache(cache, func(context.Context) string { return "k" }, cacheTTL),
	)

	_, err := p.Do(context.Background(), constFn("v1", &calls))
	require.NoError(t, err)

	got, err := p.Do(ForceRefresh(context.Background()), constFn("v2", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v2", got)
	assert.Equal(t, int64(2), calls.Load())
}

func TestWithCacheMetrics(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	clk := newPolicyClock()

	p := NewPolicy[string](
		"cache-metrics",
		WithClock(clk),
		WithCache(
			cache, func(context.Context) string { return "k" }, cacheTTL,
			StaleIfError(time.Hour),
		),
		WithRegistry(NewRegistry()),
	)

	var calls atomic.Int64

	_, _ = p.Do(context.Background(), constFn("v", &calls)) // miss + store
	_, _ = p.Do(context.Background(), constFn("v", &calls)) // hit

	clk.advance(cacheTTL + time.Minute) // stale

	_, _ = p.Do(context.Background(), errFn(errDownstream, &calls)) // stale served

	m := p.Metrics()
	assert.Equal(t, int64(1), m.CacheHits)
	assert.Equal(t, int64(2), m.CacheMisses)
	assert.Equal(t, int64(1), m.CacheStores)
	assert.Equal(t, int64(1), m.CacheStaleServed)
}

func TestWithCacheCoalesceStampede(t *testing.T) {
	t.Parallel()

	const followers = 15

	cache := newMemCache[CacheEntry[string]]()

	var joined atomic.Int64

	hooks := &Hooks{OnCoalesceFollower: func() { joined.Add(1) }}
	keyFn := func(context.Context) string { return "k" }

	p := NewPolicy[string](
		"",
		WithCache(cache, keyFn, cacheTTL),
		WithCoalesce(keyFn),
		WithTimeout(10*time.Second),
		WithHooks(hooks),
	)

	g := newGate()
	results := make(chan string, followers+1)

	// Leader: cold cache -> miss -> coalesce leader -> blocks in fn.
	go func() {
		v, err := p.Do(context.Background(), g.fn("shared"))
		assert.NoError(t, err)
		results <- v
	}()
	<-g.started

	// Followers pile onto the same in-flight key while the leader is blocked.
	for range followers {
		go func() {
			v, err := p.Do(context.Background(), g.fn("unused"))
			assert.NoError(t, err)
			results <- v
		}()
	}

	require.Eventually(t, func() bool { return joined.Load() == followers },
		waitTimeout, waitTick)
	close(g.release)

	for range followers + 1 {
		assert.Equal(t, "shared", <-results)
	}

	assert.Equal(t, int64(1), g.calls.Load(),
		"cache miss + coalesce collapse the stampede to one downstream call")
}

func TestWithCacheValidationPanics(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	keyFn := func(context.Context) string { return "k" }

	assert.PanicsWithValue(t, ErrCacheNilKeyFunc, func() {
		NewPolicy[string]("", WithCache(cache, nil, cacheTTL))
	})

	assert.PanicsWithValue(t, ErrCacheNilCache, func() {
		NewPolicy[string](
			"",
			WithCache[string](nil, keyFn, cacheTTL),
		)
	})

	assert.PanicsWithValue(t, ErrCacheNonPositiveTTL, func() {
		NewPolicy[string]("", WithCache(cache, keyFn, 0))
	})
}

func TestWithCacheTypeMismatchPanics(t *testing.T) {
	t.Parallel()

	// A cache typed for string passed to an int policy is a programmer error.
	cache := newMemCache[CacheEntry[string]]()
	keyFn := func(context.Context) string { return "k" }

	assert.Panics(t, func() {
		NewPolicy[int]("", WithCache(cache, keyFn, cacheTTL))
	})
}

// ---------------------------------------------------------------------------
// ReadThroughCache.Do — refresh-ahead
// ---------------------------------------------------------------------------

// refreshAfter is the refresh-ahead threshold used by the tests: well inside the
// fresh cacheTTL so a clock advance can land a read in the refresh window.
const refreshAfter = 40 * time.Second

// signalRefreshed wraps a cacheCounters' hooks so the test can block until a
// background refresh has repopulated the entry (the deterministic barrier for
// the detached reload goroutine — it fires only after the successful store).
func signalRefreshed(ctr *cacheCounters) (*Hooks, <-chan struct{}) {
	done := make(chan struct{}, 1)
	hooks := ctr.hooks()
	tally := hooks.OnCacheRefreshed
	hooks.OnCacheRefreshed = func() {
		tally()
		done <- struct{}{}
	}

	return hooks, done
}

func TestReadThroughRefreshAheadServesAndReloads(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	hooks, refreshed := signalRefreshed(ctr)
	clk := newPolicyClock()
	rc := newStringRTC(cache, clk, hooks, RefreshAhead(refreshAfter))

	var calls atomic.Int64

	// Miss populates the entry at t0.
	got, err := rc.Do(context.Background(), "k", constFn("v1", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", got)

	// Age into the refresh-ahead window: still fresh, but past the threshold.
	clk.advance(45 * time.Second)

	// The read serves the current (still fresh) value immediately and kicks off a
	// detached background reload that produces v2.
	got, err = rc.Do(context.Background(), "k", constFn("v2", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", got,
		"refresh-ahead serves the current value, not the reloaded one")

	<-refreshed // wait for the detached reload to repopulate the entry

	// The reload reset the store time, so the entry is fresh again at v2.
	got, err = rc.Do(context.Background(), "k", constFn("v3", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v2", got, "the next read sees the refreshed value as a fresh hit")

	assert.Equal(t, int64(2), calls.Load(),
		"miss + one background reload; the final read is a hit")
	assert.Equal(t, int64(1), ctr.refreshed.Load())
	assert.Equal(t, int64(2), ctr.stores.Load(), "the refresh also counts as a store")
	assert.GreaterOrEqual(t, ctr.hits.Load(), int64(2))
}

func TestReadThroughRefreshAheadDedupesConcurrentReloads(t *testing.T) {
	t.Parallel()

	const readers = 12

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	hooks, refreshed := signalRefreshed(ctr)
	clk := newPolicyClock()
	rc := newStringRTC(cache, clk, hooks, RefreshAhead(refreshAfter))

	// Seed the entry, then age into the refresh window.
	_, err := rc.Do(context.Background(), "k", constFn("v1", new(atomic.Int64)))
	require.NoError(t, err)
	clk.advance(45 * time.Second)

	// The reload blocks until released, so the winning reload stays in flight
	// while every reader piles on.
	g := newGate()

	var served sync.WaitGroup

	for range readers {
		served.Add(1)

		go func() {
			defer served.Done()

			got, doErr := rc.Do(context.Background(), "k", g.fn("v2"))
			assert.NoError(t, doErr)
			assert.Equal(t, "v1", got) // every reader is served the current value
		}()
	}

	served.Wait() // all readers returned without waiting for the reload

	// Exactly one reader won the refresh slot and entered the (blocked) reload;
	// the others saw the slot held and skipped.
	<-g.started
	assert.Empty(t, g.started, "only one reload runs despite the concurrent readers")
	assert.Equal(t, int64(1), g.calls.Load())

	close(g.release)
	<-refreshed
	assert.Equal(t, int64(1), ctr.refreshed.Load())
}

func TestReadThroughRefreshAheadKeepsEntryOnReloadError(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	clk := newPolicyClock()
	rc := newStringRTC(cache, clk, ctr.hooks(), RefreshAhead(refreshAfter))

	var calls atomic.Int64

	_, err := rc.Do(context.Background(), "k", constFn("v1", &calls))
	require.NoError(t, err)
	clk.advance(45 * time.Second)

	// The reload fails: the current entry must be kept and nothing stored.
	reloadDone := make(chan struct{})
	failing := func(context.Context) (string, error) {
		calls.Add(1)
		close(reloadDone)

		return "", errDownstream
	}

	got, err := rc.Do(context.Background(), "k", failing)
	require.NoError(t, err)
	assert.Equal(t, "v1", got)

	<-reloadDone // the background reload has run and returned its error

	// The err != nil path in refresh() returns before reaching store(), so a
	// failed reload can never write: the entry is untouched and no store/refresh
	// was recorded. (This holds by the short-circuit, not by reloadDone ordering;
	// the slot-release barrier below is the synchronised post-condition.)
	entry, ok := cache.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v1", entry.value)
	require.NoError(t, entry.err)
	assert.Equal(t, int64(0), ctr.refreshed.Load(), "a failed reload fires no refresh hook")
	assert.Equal(t, int64(1), ctr.stores.Load(), "a failed reload stores nothing")
	assert.Equal(t, int64(2), calls.Load(), "miss + the failed reload both executed")

	// The slot is released even on failure, so a later in-window read can retry.
	require.Eventually(t, func() bool {
		rc.refreshMu.Lock()
		defer rc.refreshMu.Unlock()

		_, busy := rc.refreshing["k"]

		return !busy
	}, waitTimeout, waitTick)
}

func TestReadThroughRefreshSlotDedup(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	rc := newStringRTC(cache, newPolicyClock(), &Hooks{})

	assert.True(t, rc.beginRefresh("k"), "first claim wins the slot")
	assert.False(t, rc.beginRefresh("k"), "second claim sees the slot held")
	rc.endRefresh("k")
	assert.True(t, rc.beginRefresh("k"), "after release the slot is claimable again")
	rc.endRefresh("k")
}

func TestReadThroughRefreshAheadInertWhenThresholdBeyondTTL(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	ctr := &cacheCounters{}
	clk := newPolicyClock()
	// The refresh threshold sits past the fresh TTL, so no in-fresh read is ever
	// old enough to trigger a reload — refresh-ahead is effectively disabled.
	rc := newStringRTC(cache, clk, ctr.hooks(), RefreshAhead(2*cacheTTL))

	var calls atomic.Int64

	_, err := rc.Do(context.Background(), "k", constFn("v1", &calls))
	require.NoError(t, err)

	clk.advance(50 * time.Second) // still fresh (<cacheTTL), below the threshold
	got, err := rc.Do(context.Background(), "k", constFn("v2", &calls))
	require.NoError(t, err)
	assert.Equal(t, "v1", got)

	assert.Equal(t, int64(1), calls.Load(),
		"a threshold beyond the fresh TTL never triggers a reload")
	assert.Equal(t, int64(0), ctr.refreshed.Load())
}

func TestWithCacheRefreshAheadRequiresTimeout(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	keyFn := func(context.Context) string { return "k" }

	// RefreshAhead without a timeout to bound the detached reload is rejected.
	assert.PanicsWithValue(t, ErrRefreshAheadWithoutTimeout, func() {
		NewPolicy[string](
			"",
			WithCache(cache, keyFn, cacheTTL, RefreshAhead(refreshAfter)),
		)
	})

	// With a timeout, the policy builds.
	assert.NotPanics(t, func() {
		NewPolicy[string](
			"",
			WithCache(cache, keyFn, cacheTTL, RefreshAhead(refreshAfter)),
			WithTimeout(time.Second),
		)
	})

	// An inert threshold (>= ttl) can never fire a reload, so it does NOT demand a
	// timeout — the invariant matches classify's actual trigger condition.
	assert.NotPanics(t, func() {
		NewPolicy[string](
			"",
			WithCache(cache, keyFn, cacheTTL, RefreshAhead(cacheTTL)),
		)
	})

	// Plain caching (refresh-ahead disabled) needs no timeout.
	assert.NotPanics(t, func() {
		NewPolicy[string]("", WithCache(cache, keyFn, cacheTTL))
	})
}

func TestPolicyCacheRefreshAhead(t *testing.T) {
	t.Parallel()

	cache := newMemCache[CacheEntry[string]]()
	keyFn := func(context.Context) string { return "k" }
	clk := newPolicyClock()
	refreshed := make(chan struct{}, 1)
	hooks := &Hooks{OnCacheRefreshed: func() { refreshed <- struct{}{} }}

	p := NewPolicy[string](
		"",
		WithCache(cache, keyFn, cacheTTL, RefreshAhead(refreshAfter)),
		WithTimeout(10*time.Second),
		WithClock(clk),
		WithHooks(hooks),
	)

	var calls atomic.Int64

	mk := func(val string) func(context.Context) (string, error) {
		return func(context.Context) (string, error) {
			calls.Add(1)

			return val, nil
		}
	}

	got, err := p.Do(context.Background(), mk("v1"))
	require.NoError(t, err)
	assert.Equal(t, "v1", got)

	clk.advance(45 * time.Second)
	got, err = p.Do(context.Background(), mk("v2"))
	require.NoError(t, err)
	assert.Equal(t, "v1", got, "the read serves the current value")

	<-refreshed // the detached reload ran through the inner timeout and stored
	assert.Equal(t, int64(1), p.Metrics().CacheRefreshes)

	got, err = p.Do(context.Background(), mk("v3"))
	require.NoError(t, err)
	assert.Equal(t, "v2", got, "the refreshed value is served on the next read")
	assert.Equal(t, int64(2), calls.Load(), "miss + one background reload")
}
