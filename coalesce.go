package r8e

import (
	"context"
	"sync"
)

// ---------------------------------------------------------------------------
// Coalescer[T] — request coalescing (singleflight)
// ---------------------------------------------------------------------------.

type (
	// Coalescer collapses concurrent calls that share a key into a single
	// execution: the first caller for a key (the "leader") runs the work, and
	// every other caller that arrives while it is still in flight (a "follower")
	// waits and shares the leader's result instead of re-running it. This kills
	// cache-stampede load — when a hot key expires, N simultaneous misses become
	// one downstream call rather than N — and pairs naturally with the cache
	// adapters.
	//
	// Coalescing only deduplicates calls that overlap in time. Once the leader
	// finishes, its key is removed; a later call for the same key starts fresh
	// (a Coalescer is not a cache — put a cache in front or behind it for that).
	//
	// The shared work runs in its own goroutine under a context detached from
	// any single caller (context.WithoutCancel), so one caller cancelling cannot
	// abort work the whole group depends on, and the work runs to completion even
	// if every caller leaves (useful to still populate a cache for the next
	// request). Each caller, including the leader, independently stops waiting as
	// soon as its own context is done and returns ctx.Err(), so a slow leader
	// never pins a follower past its deadline.
	//
	// Detaching also strips the caller's deadline, so the shared call must be
	// bounded some other way: a leader whose fn never returns parks a goroutine
	// and wedges its key until fn does return. Inside a Policy this is enforced —
	// WithCoalesce requires a WithTimeout (NewPolicy panics otherwise). Used
	// standalone, it is the caller's responsibility to give fn a deadline.
	//
	// Construct one with NewCoalescer; it is safe for concurrent use. A single
	// Coalescer may be shared across goroutines to coordinate process-wide.
	//
	// Pattern: Singleflight — a per-key in-flight registry collapses duplicate
	// concurrent calls into one shared execution, broadcast to all waiters via a
	// completion channel.
	Coalescer[T any] struct {
		hooks    *Hooks
		inFlight map[string]*coalesceCall[T]
		mu       sync.Mutex
	}

	// coalesceCall is the shared state of one in-flight execution. val and err
	// are written by the leader's goroutine before done is closed and read by
	// waiters only after done is observed closed, so the channel close is the
	// sole happens-before edge guarding them — no extra lock is needed.
	coalesceCall[T any] struct {
		val  T
		err  error
		done chan struct{}
	}
)

// NewCoalescer creates a request coalescer. The hooks receive OnCoalesceLeader
// when a call begins a shared execution and OnCoalesceFollower when a call joins
// one already in flight; pass a non-nil *Hooks (the zero value [Hooks] is fine).
func NewCoalescer[T any](hooks *Hooks) *Coalescer[T] {
	return &Coalescer[T]{
		hooks:    hooks,
		inFlight: make(map[string]*coalesceCall[T]),
	}
}

// Do executes next once per key for a set of concurrent callers, returning the
// shared result to every caller. The first caller for an in-flight key runs
// next; any caller arriving while it runs waits for and shares the same outcome.
// The key is opaque to Do — an empty string is a valid key here (the policy
// layer assigns it the "opt out of coalescing" meaning, see [WithCoalesce]).
//
// next runs with a context detached from ctx (see [Coalescer]); ctx still gates
// this caller's own wait — if ctx is cancelled before the shared call completes,
// Do returns the zero value and ctx.Err() without disturbing the shared call.
//
//nolint:ireturn // generic type parameter T, not an interface
func (c *Coalescer[T]) Do(
	ctx context.Context,
	key string,
	next func(context.Context) (T, error),
) (T, error) {
	c.mu.Lock()

	if call, ok := c.inFlight[key]; ok {
		// A leader is already running this key; join it as a follower.
		c.mu.Unlock()
		c.hooks.emitCoalesceFollower()

		return call.await(ctx)
	}

	// No leader for this key: become the leader.
	call := &coalesceCall[T]{done: make(chan struct{})}
	c.inFlight[key] = call
	c.mu.Unlock()
	c.hooks.emitCoalesceLeader()

	// Run the shared work in its own goroutine so this caller can still abandon
	// the wait on its own context without aborting the work others depend on.
	go c.execute(ctx, key, call, next)

	return call.await(ctx)
}

// execute runs the shared work for key under a detached context and broadcasts
// the result to all waiters. The cleanup — remove the key, then signal
// completion — runs in a defer so it always happens, even on a non-local exit:
// the key is removed before completion is signalled so a call arriving after
// this point starts a fresh leader rather than joining a finished one, and no
// path can strand waiters on an unclosed channel. A panic in next is not
// recovered (a panic in the user function is a caller bug, matching every other
// pattern in this package): the deferred cleanup releases the waiters, then the
// panic propagates and crashes the process.
func (c *Coalescer[T]) execute(
	parent context.Context,
	key string,
	call *coalesceCall[T],
	next func(context.Context) (T, error),
) {
	defer func() {
		c.mu.Lock()
		delete(c.inFlight, key)
		c.mu.Unlock()

		close(call.done)
	}()

	// Detach cancellation and deadline so no single caller can abort the group's
	// work; context values (trace spans, etc.) are preserved from the leader.
	call.val, call.err = next(context.WithoutCancel(parent))
}

// await blocks until the shared call completes or ctx is done, whichever comes
// first. On the shared call completing it returns the shared result; on ctx
// being cancelled it returns the zero value and ctx.Err(), leaving the shared
// call running for the other waiters.
//
// A ready result wins a tie: if the shared call has already completed, await
// returns its result even when ctx is also done, rather than discarding
// finished work. This makes the outcome deterministic when both are ready,
// where a bare select would choose at random.
//
//nolint:ireturn // generic type parameter T, not an interface
func (cc *coalesceCall[T]) await(ctx context.Context) (T, error) {
	select {
	case <-cc.done:
		return cc.val, cc.err //nolint:wrapcheck // shared call's error returned as-is
	default:
	}

	select {
	case <-cc.done:
		return cc.val, cc.err //nolint:wrapcheck // shared call's error returned as-is
	case <-ctx.Done():
		var zero T

		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}
}

// InFlight returns the number of distinct keys currently executing as a
// point-in-time snapshot. Surfaced by Policy.Metrics as a gauge.
func (c *Coalescer[T]) InFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return len(c.inFlight)
}
