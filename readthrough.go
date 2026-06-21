package r8e

import (
	"context"
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
	// unifies three caching behaviours behind one type:
	//
	//   - Read-through: within the fresh TTL, a hit returns the cached value
	//     without executing the downstream work at all.
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
	// Revalidation runs synchronously in the caller's path under the caller's
	// context (it is not detached, unlike [Coalescer]); pair the cache with
	// [WithCoalesce] so a burst of concurrent misses or stale revalidations for a
	// hot key collapses into a single downstream call rather than a stampede.
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
		cache    Cache[string, CacheEntry[T]]
		hooks    *Hooks
		clock    Clock
		freshTTL time.Duration
		staleTTL time.Duration
		negTTL   time.Duration
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
		clock    Clock
		hooks    *Hooks
		staleTTL time.Duration
		negTTL   time.Duration
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
		cache:    cache,
		hooks:    cfg.hooks,
		clock:    cfg.clock,
		freshTTL: ttl,
		staleTTL: cfg.staleTTL,
		negTTL:   cfg.negTTL,
	}
}

// Do serves key from the cache or executes next to populate it. An empty key
// opts the call out of caching entirely — next runs and nothing is cached.
//
// On a fresh hit (or a valid negative entry) Do returns the cached outcome
// without calling next. Otherwise it executes next and: on success caches and
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
