package r8e

import (
	"context"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// ReadThroughCache[T] — read-through result cache with stale-if-error and
// negative caching
// ---------------------------------------------------------------------------.

type (
	// ReadThroughCache memoizes successful results of a keyed call. On a fresh
	// hit it short-circuits the work entirely and returns the cached value; on a
	// miss it executes the work and caches a successful result for the TTL. It
	// unifies four caching behaviours behind one type:
	//
	//   - Read-through: within the fresh TTL, a hit returns the cached value
	//     without executing the downstream work at all.
	//   - Refresh-ahead: with [RefreshAhead] set, a hit landing in the tail of the
	//     fresh window (past refreshTTL but still fresh) is served immediately and
	//     additionally kicks off a single, coalesced background reload that
	//     repopulates the entry before it expires — so a hot key keeps serving
	//     fresh hits rather than falling through to a synchronous miss at expiry
	//     (Caffeine refreshAfterWrite semantics; see [RefreshAhead] for the
	//     timing caveats).
	//   - Stale-if-error: with [StaleIfError] set, a value lingers past its fresh
	//     TTL as a stale fallback. A call landing in the stale window re-executes
	//     to refresh, but if that execution fails the stale value is served
	//     instead of surfacing the error — masking a downstream outage with the
	//     last good result (RFC 5861 stale-if-error semantics).
	//   - Negative caching: with [NegativeCache] set, a failed execution with no
	//     stale value to fall back on is itself cached for a short TTL, so repeated
	//     calls for a known-bad key fast-fail with the recorded error instead of
	//     hammering a failing downstream.
	//
	// Freshness is measured against the injected [Clock], not the underlying
	// cache's own expiry, so behaviour is deterministic under a fake clock. The
	// underlying [Cache] is parameterised by [CacheEntry] (e.g.
	// otter.MustNew[string, r8e.CacheEntry[T]]): each stored value carries the
	// metadata — store time and any recorded error — that distinguishes a fresh
	// hit, a stale fallback, and a negative entry.
	//
	// Miss and stale revalidation run synchronously in the caller's path under
	// the caller's context (not detached, unlike [Coalescer]); pair the cache
	// with [WithCoalesce] so a burst of concurrent misses or stale revalidations
	// for a hot key collapses into a single downstream call rather than a
	// stampede. A [RefreshAhead] reload is the exception: it runs in a detached
	// background goroutine (deadline stripped, like [Coalescer]) and is
	// deduplicated internally so only one reload per key is ever in flight; within
	// a [Policy] [WithCache] requires a [WithTimeout] to bound that detached call.
	//
	// Construct one with [NewReadThroughCache]; it is safe for concurrent use as
	// long as the underlying [Cache] is. For a simpler standalone (non-[Policy])
	// stale-on-error wrapper over an arbitrary comparable key type, see
	// [StaleCache], which this supersedes for in-chain caching.
	//
	// Pattern: Read-through cache — reads are served from the cache, and the
	// cache itself populates on miss by invoking the wrapped work, so callers
	// never see the loader directly.
	ReadThroughCache[T any] struct {
		cache      Cache[string, CacheEntry[T]]
		hooks      *Hooks
		clock      Clock
		refreshing map[string]struct{}
		freshTTL   time.Duration
		staleTTL   time.Duration
		negTTL     time.Duration
		refreshTTL time.Duration
		refreshMu  sync.Mutex
	}

	// CacheEntry wraps a cached value with the metadata [ReadThroughCache] needs
	// to tell a fresh hit, a stale fallback, and a negative (cached-error) entry
	// apart. It is an opaque instantiation token: every field is unexported and
	// written only by the cache itself, so it has no caller-usable surface by
	// design. The type is exported solely so the underlying [Cache] can be
	// instantiated with it, e.g. otter.MustNew[string, r8e.CacheEntry[User]](cfg);
	// you never construct, read, or mutate one yourself.
	CacheEntry[T any] struct {
		// err records the error of a negative entry; nil for a success entry.
		err error
		// storedAt is the [Clock] time the entry was written, used to compute its
		// age against the fresh, stale, and negative TTLs.
		storedAt time.Time
		// value is the cached result; the zero value for a negative entry.
		value T
	}

	// CacheOption configures the optional behaviours of a [ReadThroughCache] /
	// [WithCache]: stale-if-error and negative caching. Read-through with the
	// fresh TTL is always on; these options extend it.
	CacheOption func(*cacheOptions)

	// cacheOptions accumulates [CacheOption] values before they are folded into a
	// [ReadThroughCache]. The durations default to zero (disabling the
	// corresponding behaviour); clock and hooks default to [RealClock] and the
	// no-op zero [Hooks] in [NewReadThroughCache].
	cacheOptions struct {
		clock      Clock
		hooks      *Hooks
		staleTTL   time.Duration
		negTTL     time.Duration
		refreshTTL time.Duration
	}

	// forceRefreshKey is the private context key under which [ForceRefresh] marks
	// a call to bypass the cache read.
	forceRefreshKey struct{}

	// entryState classifies a cache entry against the [Clock] at lookup time, so
	// the fresh/stale/negative/miss decision is computed once where the entry's
	// age and error live, rather than reconstructed by the caller.
	entryState int
)

const (
	// entryMiss means no logically valid entry exists (absent, or aged out of
	// every window even if the backing cache still holds it).
	entryMiss entryState = iota
	// entryFresh means a success entry within its fresh TTL — a read-through hit.
	entryFresh
	// entryRefreshAhead means a success entry still within its fresh TTL but past
	// its refresh threshold (see [RefreshAhead]): served as a hit, and triggers a
	// single coalesced background reload to repopulate it before it expires.
	entryRefreshAhead
	// entryStale means a success entry past its fresh TTL but within the stale
	// window — revalidate, and serve it if revalidation fails.
	entryStale
	// entryNegativeHit means a valid negative (cached-error) entry — fast-fail.
	entryNegativeHit
)

// StaleIfError enables stale-if-error: a cached value remains usable for
// staleTTL beyond its fresh TTL as a fallback. A call arriving in that stale
// window re-executes to refresh the value, but if the execution fails the stale
// value is served (and OnStaleServed fires) instead of returning the error.
// A non-positive staleTTL leaves stale-if-error disabled.
func StaleIfError(staleTTL time.Duration) CacheOption {
	return func(o *cacheOptions) {
		o.staleTTL = staleTTL
	}
}

// RefreshAhead enables refresh-ahead (Caffeine refreshAfterWrite semantics): a
// hit whose age has passed refreshTTL but is still within the fresh TTL is
// served immediately and additionally triggers a single background reload that
// repopulates the entry before it expires, so a hot key keeps serving fresh
// hits rather than falling through to a synchronous miss when the entry expires.
// The reload runs in a detached goroutine (the triggering caller is not blocked)
// and is deduplicated per key, so a burst of reads in the refresh window starts
// at most one reload. A failed reload is best-effort: the current entry is kept
// and the next read in the window retries; a successful reload fires
// OnCacheRefreshed (and counts as a store).
//
// The reload pre-empts a miss only when it finishes before the entry leaves the
// fresh window; with a slow reload (or a refresh threshold close to ttl) a read
// can still reach a stale revalidation or a synchronous miss while the reload is
// in flight, and the dedup covers only refresh-vs-refresh, not a refresh racing
// a concurrent miss or [ForceRefresh]. Because the reload stores with its own
// completion time, a slow reload returning an older value can also overwrite a
// newer one written by a concurrent [ForceRefresh] in that window (last write by
// store time wins).
//
// The detached reload's deadline is stripped, so it must be bounded some other
// way. Within a [Policy] this is enforced: [WithCache] with a firing refresh
// threshold requires a [WithTimeout] (the inner timeout bounds the reload), else
// [NewPolicy] panics with [ErrRefreshAheadWithoutTimeout]. Used standalone via
// [NewReadThroughCache], it is the caller's responsibility to give the loader a
// deadline — an unbounded loader that never returns parks the reload goroutine
// and wedges that key's refresh slot.
//
// refreshTTL should be shorter than the fresh ttl; a non-positive refreshTTL, or
// one at or beyond ttl, leaves refresh-ahead disabled (no in-fresh-window read
// can ever be old enough to trigger it, and within a [Policy] such an inert
// configuration does not demand a [WithTimeout]).
func RefreshAhead(refreshTTL time.Duration) CacheOption {
	return func(o *cacheOptions) {
		o.refreshTTL = refreshTTL
	}
}

// CacheClock sets the [Clock] a [ReadThroughCache] uses to measure freshness.
// It defaults to [RealClock]; a nil clock is ignored. Within a [Policy] the
// policy's clock (see [WithClock]) is injected automatically, so this is for
// standalone [NewReadThroughCache] use — chiefly deterministic tests.
func CacheClock(c Clock) CacheOption {
	return func(o *cacheOptions) {
		if c != nil {
			o.clock = c
		}
	}
}

// CacheHooks sets the [Hooks] a [ReadThroughCache] emits cache events to. It
// defaults to the no-op zero [Hooks]; a nil argument is ignored. Within a
// [Policy] the policy's hooks (see [WithHooks]) are injected automatically, so
// this is for standalone [NewReadThroughCache] use.
func CacheHooks(h *Hooks) CacheOption {
	return func(o *cacheOptions) {
		if h != nil {
			o.hooks = h
		}
	}
}

// NegativeCache enables negative caching: when an execution fails and no stale
// value is available to serve, the error is cached for negTTL so subsequent
// calls for the same key fast-fail with the recorded error instead of
// re-executing. Keep negTTL short — it suppresses recovery for that long. A
// non-positive negTTL leaves negative caching disabled.
func NegativeCache(negTTL time.Duration) CacheOption {
	return func(o *cacheOptions) {
		o.negTTL = negTTL
	}
}

// ForceRefresh returns a child context that makes the next [ReadThroughCache]
// (or [WithCache]) call bypass the cache read: it skips the fresh hit and any
// stale fallback, executes the work, and repopulates the cache on success. Use
// it to bust a cached entry for a single call without disturbing other callers.
func ForceRefresh(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceRefreshKey{}, true)
}

// isForceRefresh reports whether ctx was marked by [ForceRefresh].
func isForceRefresh(ctx context.Context) bool {
	forced, ok := ctx.Value(forceRefreshKey{}).(bool)

	return ok && forced
}

// NewReadThroughCache creates a read-through cache backed by the given [Cache].
// ttl is the fresh window: within it a hit is served without re-executing. With
// [StaleIfError] the entry then lingers as a stale fallback for an extra
// staleTTL, so its total lifetime in the backing cache is ttl+staleTTL. By
// default freshness is measured with [RealClock] and no cache events are
// emitted; override these with [CacheClock] and [CacheHooks], and enable the
// optional behaviours with [StaleIfError] and [NegativeCache].
func NewReadThroughCache[T any](
	cache Cache[string, CacheEntry[T]],
	ttl time.Duration,
	opts ...CacheOption,
) *ReadThroughCache[T] {
	cfg := cacheOptions{clock: RealClock{}, hooks: &Hooks{}}
	for _, opt := range opts {
		opt(&cfg)
	}

	return &ReadThroughCache[T]{
		cache:      cache,
		hooks:      cfg.hooks,
		clock:      cfg.clock,
		refreshing: make(map[string]struct{}),
		freshTTL:   ttl,
		staleTTL:   cfg.staleTTL,
		negTTL:     cfg.negTTL,
		refreshTTL: cfg.refreshTTL,
	}
}

// Do serves key from the cache or executes next to populate it. An empty key
// opts the call out of caching entirely — next runs and nothing is cached.
//
// On a fresh hit (or a valid negative entry) Do returns the cached outcome
// without calling next. A fresh hit in the refresh-ahead window (see
// [RefreshAhead]) additionally kicks off a single detached background reload of
// next before returning. Otherwise it executes next and: on success caches and
// returns the result; on error serves a stale value if one is available (see
// [StaleIfError]), else caches the error if negative caching is enabled (see
// [NegativeCache]) and returns it.
//
//nolint:ireturn // generic type parameter T, not an interface
func (rc *ReadThroughCache[T]) Do(
	ctx context.Context,
	key string,
	next func(context.Context) (T, error),
) (T, error) {
	if key == "" {
		return next(ctx) //nolint:wrapcheck // pass-through: caller's error as-is
	}

	var (
		staleValue T
		haveStale  bool
	)

	if !isForceRefresh(ctx) {
		switch entry, state := rc.classify(key); state {
		case entryNegativeHit: // valid negative entry: fast-fail
			rc.hooks.emitCacheHit()

			var zero T

			return zero, entry.err //nolint:wrapcheck // recorded error replayed as-is
		case entryFresh: // fresh success: short-circuit
			rc.hooks.emitCacheHit()

			return entry.value, nil
		case entryRefreshAhead: // fresh but ageing: serve now, refresh in background
			rc.hooks.emitCacheHit()
			rc.triggerRefresh(ctx, key, next)

			return entry.value, nil
		case entryStale: // stale success: revalidate with stale fallback
			staleValue, haveStale = entry.value, true
		default: // entryMiss: no usable entry — fall through to execute and populate
		}
	}

	rc.hooks.emitCacheMiss()

	result, err := next(ctx)

	switch {
	case err == nil:
		rc.store(key, result)

		return result, nil
	case haveStale:
		rc.hooks.emitStaleServed()

		return staleValue, nil
	default:
		if rc.negTTL > 0 {
			rc.storeNegative(key, err)
		}

		var zero T

		return zero, err //nolint:wrapcheck // downstream error returned as-is
	}
}

// classify fetches the entry for key and returns it together with its state
// against the [Clock] — computed once, here, where the entry's age and error
// live. Freshness is clock-driven and independent of the backing cache's own
// expiry: an entry the cache still physically holds but which has aged out of
// every window is reported [entryMiss]. The returned entry is meaningful only
// for the [entryFresh], [entryStale], and [entryNegativeHit] states.
func (rc *ReadThroughCache[T]) classify(key string) (CacheEntry[T], entryState) {
	entry, ok := rc.cache.Get(key)
	if !ok {
		return entry, entryMiss
	}

	age := rc.clock.Since(entry.storedAt)

	switch {
	case entry.err != nil:
		if age < rc.negTTL {
			return entry, entryNegativeHit
		}

		return entry, entryMiss
	case age < rc.freshTTL:
		if rc.refreshTTL > 0 && age >= rc.refreshTTL {
			return entry, entryRefreshAhead
		}

		return entry, entryFresh
	case age < rc.freshTTL+rc.staleTTL:
		return entry, entryStale
	default:
		return entry, entryMiss
	}
}

// store caches a successful result for the fresh+stale lifetime and fires
// OnCacheStored. The underlying TTL spans both windows so the value survives as
// a stale fallback after its fresh window; where it falls within that span
// (fresh vs stale) is decided by classify on the next lookup.
func (rc *ReadThroughCache[T]) store(key string, result T) {
	rc.cache.Set(
		key,
		CacheEntry[T]{value: result, storedAt: rc.clock.Now()},
		rc.freshTTL+rc.staleTTL,
	)
	rc.hooks.emitCacheStored()
}

// storeNegative caches a failed execution for the negative TTL so subsequent
// calls for key fast-fail with err. Only reached when negative caching is
// enabled and no stale value was available.
func (rc *ReadThroughCache[T]) storeNegative(key string, callErr error) {
	rc.cache.Set(
		key,
		CacheEntry[T]{err: callErr, storedAt: rc.clock.Now()},
		rc.negTTL,
	)
}

// triggerRefresh starts a background reload of key if one is not already in
// flight (see [RefreshAhead]). It is fire-and-forget: the triggering caller has
// already been served the still-fresh value and does not wait for the reload.
func (rc *ReadThroughCache[T]) triggerRefresh(
	ctx context.Context,
	key string,
	next func(context.Context) (T, error),
) {
	if !rc.beginRefresh(key) {
		return // a reload for this key is already running — dedup the stampede
	}

	go rc.refresh(ctx, key, next)
}

// beginRefresh claims the sole refresh slot for key, reporting whether this
// caller won it. Only the winner spawns the reload goroutine; concurrent
// callers in the refresh window see the key already claimed and skip, so at most
// one reload per key is ever in flight.
func (rc *ReadThroughCache[T]) beginRefresh(key string) bool {
	rc.refreshMu.Lock()
	defer rc.refreshMu.Unlock()

	if _, refreshing := rc.refreshing[key]; refreshing {
		return false
	}

	rc.refreshing[key] = struct{}{}

	return true
}

// endRefresh releases key's refresh slot so a later read in the refresh window
// can trigger a fresh reload.
func (rc *ReadThroughCache[T]) endRefresh(key string) {
	rc.refreshMu.Lock()
	delete(rc.refreshing, key)
	rc.refreshMu.Unlock()
}

// refresh runs the background reload for key and repopulates the cache on
// success. It executes next under a context detached from the triggering caller
// (cancellation and deadline stripped, values preserved) so the caller leaving
// cannot abort the shared refresh — the inner [WithTimeout] the [Policy] requires
// bounds it instead. A failed reload is best-effort: the current entry is left
// untouched (its store time unchanged, so the next in-window read retries) and
// no error is surfaced. A successful reload stores the value (firing
// OnCacheStored) and fires OnCacheRefreshed.
//
// The deferred endRefresh always releases the slot, so even a panic in next
// cannot wedge the key. The panic itself is NOT recovered here (a panic in user
// code is a caller bug, as everywhere else in this package): after the slot is
// released it propagates out of this detached goroutine and crashes the process,
// unless an inner [WithRecover] stage — which sits inside the cache and so wraps
// next — has already converted it to an error.
func (rc *ReadThroughCache[T]) refresh(
	parent context.Context,
	key string,
	next func(context.Context) (T, error),
) {
	defer rc.endRefresh(key)

	result, err := next(context.WithoutCancel(parent))
	if err != nil {
		return // keep the current entry; a later read retries the refresh
	}

	rc.store(key, result)
	rc.hooks.emitCacheRefreshed()
}
