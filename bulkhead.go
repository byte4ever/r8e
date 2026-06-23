package r8e

import (
	"context"
	"sync"
	"time"
)

type (
	// Bulkhead limits concurrent access to a resource.
	//
	// Pattern: Bulkhead — concurrency limiter that prevents resource exhaustion.
	// By default it rejects immediately with [ErrBulkheadFull] when all slots are
	// in use. With a positive max-wait (see [BulkheadMaxWait]) a full bulkhead
	// instead queues callers in FIFO order up to a bounded depth (see
	// [BulkheadQueueDepth]), handing each freed slot to the head of the queue; a
	// caller that waits longer than max-wait gives up with [ErrBulkheadTimeout],
	// and one whose context is cancelled while queued returns the context error.
	//
	// The controlled-delay discipline (see [BulkheadCoDel]) is an alternative — or
	// addition — to the fixed max-wait: it watches the standing queue delay and,
	// while the queue stays persistently backed up, sheds callers that have waited
	// past the slough timeout ([ErrCoDelShed]) and serves the newest callers first
	// (adaptive LIFO). It enables the wait on its own, so a bulkhead with only
	// BulkheadCoDel still queues; a max-wait set alongside it acts as a hard
	// ceiling on top of the adaptive shedding.
	//
	// State (the in-use count and the wait queue) is guarded by a mutex so the
	// tuple mutates atomically; each waiter's grant channel is closed under that
	// lock so a Release and a concurrent timeout/cancel can never both claim the
	// same slot.
	Bulkhead struct {
		clock   Clock
		hooks   *Hooks
		waiters []*bulkheadWaiter
		codel   codel

		mu       sync.Mutex
		maxConc  int
		cur      int
		maxWait  time.Duration
		maxQueue int
	}

	// bulkheadWaiter is one caller parked in the wait queue. ready is closed (once,
	// under the bulkhead mutex) to wake it; shed, written before that close, tells
	// the woken caller whether it was granted a slot (false) or dropped by the
	// controlled-delay discipline (true). enqueued stamps when it joined the queue,
	// for the CoDel dwell measurement.
	bulkheadWaiter struct {
		enqueued time.Time
		ready    chan struct{}
		shed     bool
	}

	// BulkheadOption configures optional [Bulkhead] behaviour — the bounded wait,
	// either the fixed max-wait or the controlled-delay discipline. With no options
	// the bulkhead keeps the default reject-immediately semantics.
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config, keeping NewBulkhead's signature stable as options are added.
	BulkheadOption func(*bulkheadConfig)

	bulkheadConfig struct {
		maxWait       time.Duration
		maxQueue      int
		codelTarget   time.Duration
		codelInterval time.Duration
	}
)

// BulkheadMaxWait enables the bounded FIFO wait: a full bulkhead queues callers
// for up to d instead of rejecting immediately. A non-positive d (the default)
// keeps the reject-immediately behaviour. The wait is measured with the
// bulkhead's Clock.
func BulkheadMaxWait(d time.Duration) BulkheadOption {
	return func(c *bulkheadConfig) {
		c.maxWait = d
	}
}

// BulkheadQueueDepth caps how many callers may wait at once; once the queue is at
// this depth a full bulkhead rejects immediately with [ErrBulkheadFull] rather
// than growing without bound. Values below 1 are ignored. Defaults to the
// bulkhead's max-concurrency. Has no effect unless a wait is enabled by
// [BulkheadMaxWait] or [BulkheadCoDel].
func BulkheadQueueDepth(n int) BulkheadOption {
	return func(c *bulkheadConfig) {
		if n >= 1 {
			c.maxQueue = n
		}
	}
}

// BulkheadCoDel enables the controlled-delay (CoDel) queue discipline on the
// bulkhead's bounded wait, after RFC 8289 and Facebook's folly executor. It is
// an admission control for the wait queue tuned by how long callers actually
// dwell, rather than a fixed per-caller deadline:
//
//   - target is the acceptable standing queue delay. While the dwell of the
//     oldest queued caller stays at or below target the queue is healthy: callers
//     are served oldest-first (FIFO) and none are shed.
//   - interval is how long that standing delay must persist above target before
//     the queue is declared overloaded. Once overloaded, callers that have waited
//     past the slough timeout (2 × target) are shed with [ErrCoDelShed], and the
//     remaining callers are served newest-first (adaptive LIFO) — keeping the
//     freshest, likeliest-still-wanted callers moving while the stale ones, whose
//     clients have probably given up, are dropped. A single sample back at or
//     below target clears the overload and restores FIFO.
//
// The folly defaults are target 5ms, interval 100ms. CoDel enables the wait on
// its own (a bulkhead with only BulkheadCoDel still queues); combine it with
// [BulkheadMaxWait] to also cap the absolute wait, and [BulkheadQueueDepth] to
// bound the queue length. Non-positive target or interval is ignored, leaving
// the discipline off. The dwell is measured with the bulkhead's Clock.
func BulkheadCoDel(target, interval time.Duration) BulkheadOption {
	return func(c *bulkheadConfig) {
		if target > 0 && interval > 0 {
			c.codelTarget = target
			c.codelInterval = interval
		}
	}
}

// NewBulkhead creates a bulkhead that allows at most maxConcurrent simultaneous
// calls, using clock for max-wait timing (like the other limiters; a [Policy]
// injects its own clock). By default a full bulkhead rejects immediately; pass
// [BulkheadMaxWait] to enable the bounded FIFO wait.
func NewBulkhead(
	maxConcurrent int,
	clock Clock,
	hooks *Hooks,
	opts ...BulkheadOption,
) *Bulkhead {
	var cfg bulkheadConfig
	for _, o := range opts {
		o(&cfg)
	}

	return &Bulkhead{
		clock:    clock,
		hooks:    hooks,
		maxConc:  maxConcurrent,
		maxWait:  cfg.maxWait,
		maxQueue: effectiveQueueDepth(cfg.maxQueue, maxConcurrent),
		codel:    codel{target: cfg.codelTarget, interval: cfg.codelInterval},
	}
}

// effectiveQueueDepth defaults an unset (<=0) queue depth to the concurrency
// limit, so [BulkheadMaxWait] alone yields a usable bounded queue.
func effectiveQueueDepth(maxQueue, maxConcurrent int) int {
	if maxQueue <= 0 {
		return maxConcurrent
	}

	return maxQueue
}

// Reconfigure changes the bulkhead's limits at runtime. The concurrency limit is
// replaced; max-wait, queue depth and the CoDel target/interval from opts are
// applied (unset options keep their current value); the clock is not
// reconfigurable. In-flight calls are unaffected. If the new concurrency limit
// opened capacity, queued waiters are granted slots in FIFO order. Changing the
// CoDel target/interval resets its overload latch.
func (b *Bulkhead) Reconfigure(maxConcurrent int, opts ...BulkheadOption) {
	b.mu.Lock()
	defer b.mu.Unlock()

	cfg := bulkheadConfig{
		maxWait:       b.maxWait,
		maxQueue:      b.maxQueue,
		codelTarget:   b.codel.target,
		codelInterval: b.codel.interval,
	}
	for _, o := range opts {
		o(&cfg)
	}

	b.maxConc = maxConcurrent
	b.maxWait = cfg.maxWait
	b.maxQueue = effectiveQueueDepth(cfg.maxQueue, maxConcurrent)
	b.codel.reconfigure(cfg.codelTarget, cfg.codelInterval)
	b.drainWaiters()
}

// Acquire reserves a slot, blocking until one is free if the bulkhead is full
// and a wait is enabled (see [BulkheadMaxWait] and [BulkheadCoDel]). It returns:
//   - nil once a slot is held (release it with [Bulkhead.Release]);
//   - [ErrBulkheadFull] if full and either no wait is enabled or the wait queue
//     is already at its bounded depth;
//   - [ErrBulkheadTimeout] if the caller waited the full max-wait without a slot;
//   - [ErrCoDelShed] if the controlled-delay discipline shed the caller because
//     the queue was overloaded and it had waited past the slough timeout;
//   - ctx.Err() if ctx is cancelled while waiting.
func (b *Bulkhead) Acquire(ctx context.Context) error {
	b.mu.Lock()

	if b.cur < b.maxConc {
		b.cur++
		b.mu.Unlock()
		b.hooks.emitBulkheadAcquired()

		return nil
	}

	if len(b.waiters) >= b.maxQueue || !b.queueable() {
		b.mu.Unlock()
		b.hooks.emitBulkheadFull()

		return ErrBulkheadFull
	}

	w := &bulkheadWaiter{ready: make(chan struct{}), enqueued: b.clock.Now()}
	b.waiters = append(b.waiters, w)
	maxWait := b.maxWait // capture under the lock; Reconfigure may change it
	b.mu.Unlock()
	b.hooks.emitBulkheadQueued()

	return b.waitForSlot(ctx, w, maxWait)
}

// queueable reports whether a full bulkhead should enqueue a caller rather than
// reject immediately: either the fixed max-wait or the controlled-delay
// discipline is enabled.
func (b *Bulkhead) queueable() bool {
	return b.maxWait > 0 || b.codel.enabled()
}

// waitForSlot blocks until the queued waiter w is handed a slot, shed by the
// controlled-delay discipline, the max-wait elapses, or ctx is cancelled.
// Exactly one outcome wins; the abandon check resolves any race with a
// concurrent dequeue. maxWait is captured by the caller under the lock so this
// runs without touching shared state until it re-locks via abandon/releaseSlot.
// A non-positive max-wait (CoDel-only queue) installs no timer, so the caller
// waits until granted, shed, or cancelled.
func (b *Bulkhead) waitForSlot(
	ctx context.Context,
	waiter *bulkheadWaiter,
	maxWait time.Duration,
) error {
	var timeout <-chan time.Time

	if maxWait > 0 {
		timer := b.clock.NewTimer(maxWait)
		defer timer.Stop()

		timeout = timer.C()
	}

	select {
	case <-waiter.ready:
		return b.resolveOutcome(waiter)

	case <-timeout:
		if b.abandon(waiter) {
			b.hooks.emitBulkheadTimeout()

			return ErrBulkheadTimeout
		}
		// Woken (granted or shed) just as the timer fired — honour the outcome.
		return b.resolveOutcome(waiter)

	case <-ctx.Done():
		if b.abandon(waiter) {
			return ctx.Err() //nolint:wrapcheck // preserve context error identity
		}
		// Woken just as ctx was cancelled. A shed owns no slot; a grant hands us
		// one we will not use, so return it without an acquired/released pair.
		if waiter.shed {
			b.hooks.emitCoDelShed()

			return ErrCoDelShed
		}

		b.releaseSlot()

		return ctx.Err() //nolint:wrapcheck // preserve context error identity
	}
}

// resolveOutcome interprets a closed grant signal. A controlled-delay shed
// returns [ErrCoDelShed]; otherwise the caller now holds a slot. w.shed is set
// under mu before the channel close, so reading it after the receive is
// race-free (the close establishes the happens-before edge).
func (b *Bulkhead) resolveOutcome(w *bulkheadWaiter) error {
	if w.shed {
		b.hooks.emitCoDelShed()

		return ErrCoDelShed
	}

	b.hooks.emitBulkheadAcquired()

	return nil
}

// abandon removes the still-queued waiter (it timed out or was cancelled). It
// returns true if the waiter was removed, or false if a concurrent dequeue had
// already granted or shed it (and removed it from the queue), in which case its
// outcome is settled in w.shed. Runs under mu, paired with the dequeue removing
// the waiter under mu, so exactly one of abandon/dequeue claims it.
func (b *Bulkhead) abandon(target *bulkheadWaiter) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, w := range b.waiters {
		if w == target {
			b.waiters = removeWaiterAt(b.waiters, i)

			return true
		}
	}

	return false
}

// Release releases a slot previously taken by a successful [Bulkhead.Acquire]. A
// Release with no matching Acquire (or a double Release) is a no-op rather than
// driving the counter negative, which would silently disable the limiter. If
// callers are queued, the freed slot is handed to a waiter instead of being
// returned to the pool.
func (b *Bulkhead) Release() {
	if b.releaseSlot() {
		b.hooks.emitBulkheadReleased()
	}
}

// releaseSlot returns one held slot: it hands the slot to a queued waiter if any
// remain after the controlled-delay pass, otherwise decrements the in-use count.
// It reports whether a slot was actually released (handed off or decremented) so
// a spurious Release stays a silent no-op.
func (b *Bulkhead) releaseSlot() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.handOffLocked()
}

// handOffLocked routes a freed slot. It first runs the controlled-delay pass,
// which refreshes the overload latch and sheds any stale waiters, then hands the
// slot to the next waiter — newest-first while overloaded (adaptive LIFO),
// oldest-first while healthy (FIFO) — or returns it to the pool when the queue is
// empty. Caller must hold mu.
func (b *Bulkhead) handOffLocked() bool {
	b.codelShedStaleLocked()

	if len(b.waiters) > 0 {
		b.grantWaiterAt(b.nextWaiterIndexLocked())

		return true
	}

	if b.cur > 0 {
		b.cur--

		return true
	}

	return false
}

// codelShedStaleLocked refreshes the controlled-delay overload latch from the
// standing queue delay and, while overloaded, sheds every front waiter whose
// dwell has passed the slough timeout — the controlled-delay drop of callers
// that have already waited too long to be worth serving. Each shed wakes its
// waiter with [ErrCoDelShed]. No-op when CoDel is disabled. Caller must hold mu.
func (b *Bulkhead) codelShedStaleLocked() {
	if !b.codel.enabled() {
		return
	}

	now := b.clock.Now()
	b.codel.observe(b.standingDelayLocked(now), now)

	if !b.codel.overloaded {
		return
	}

	slough := b.codel.sloughTimeout()
	for len(b.waiters) > 0 && now.Sub(b.waiters[0].enqueued) > slough {
		b.shedWaiterAt(0)
	}
}

// nextWaiterIndexLocked picks which queued waiter receives a freed slot: the
// newest (tail) while CoDel reports the queue overloaded — adaptive LIFO keeps
// the freshest, likeliest-still-wanted callers moving — otherwise the oldest
// (head), plain FIFO. Caller must hold mu and ensure the queue is non-empty.
func (b *Bulkhead) nextWaiterIndexLocked() int {
	if b.codel.isOverloaded() {
		return len(b.waiters) - 1
	}

	return 0
}

// standingDelayLocked is the dwell of the oldest queued waiter — the standing
// queue delay the controlled-delay discipline watches — or 0 when the queue is
// empty. Caller must hold mu.
func (b *Bulkhead) standingDelayLocked(now time.Time) time.Duration {
	if len(b.waiters) == 0 {
		return 0
	}

	return now.Sub(b.waiters[0].enqueued)
}

// grantWaiterAt hands the freed slot to waiter idx, waking it to return from
// Acquire with the slot held. Caller must hold mu; idx must be in range.
func (b *Bulkhead) grantWaiterAt(idx int) {
	b.waiters[idx].shed = false
	b.closeWaiterAt(idx)
}

// shedWaiterAt drops waiter idx under controlled-delay overload, waking it to
// return [ErrCoDelShed] without a slot. Caller must hold mu; idx must be in range.
func (b *Bulkhead) shedWaiterAt(idx int) {
	b.waiters[idx].shed = true
	b.closeWaiterAt(idx)
}

// closeWaiterAt removes waiter idx and wakes it by closing its grant channel. The
// caller sets w.shed before this so the woken waiter reads the outcome race-free
// (the close establishes the happens-before edge). Caller must hold mu.
func (b *Bulkhead) closeWaiterAt(idx int) {
	close(b.waiters[idx].ready)

	b.waiters = removeWaiterAt(b.waiters, idx)
}

// drainWaiters hands newly opened capacity to queued callers in FIFO order, used
// after a concurrency-limit increase. Each grant consumes a fresh slot (cur++),
// unlike a Release handoff which transfers an existing slot. A capacity increase
// is a recovery signal, so it grants oldest-first and does not shed. Caller must
// hold mu.
func (b *Bulkhead) drainWaiters() {
	for b.cur < b.maxConc && len(b.waiters) > 0 {
		b.cur++

		b.grantWaiterAt(0)
	}
}

// removeWaiterAt removes index i from the waiter slice, preserving order and
// niling the freed tail slot so the dropped waiter can be garbage collected.
func removeWaiterAt(waiters []*bulkheadWaiter, i int) []*bulkheadWaiter {
	copy(waiters[i:], waiters[i+1:])
	waiters[len(waiters)-1] = nil

	return waiters[:len(waiters)-1]
}

// Full returns true if all slots are in use. Callers may still be queued behind a
// full bulkhead when a max-wait is configured.
func (b *Bulkhead) Full() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.cur >= b.maxConc
}

// InUse returns the number of slots currently held.
func (b *Bulkhead) InUse() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	return int64(b.cur)
}

// Cap returns the configured maximum number of concurrent slots.
func (b *Bulkhead) Cap() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	return int64(b.maxConc)
}

// Queued returns the number of callers currently waiting for a slot; 0 unless a
// wait is enabled (see [BulkheadMaxWait] and [BulkheadCoDel]).
func (b *Bulkhead) Queued() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	return int64(len(b.waiters))
}

// Overloaded reports whether the controlled-delay discipline currently sees the
// wait queue as overloaded — its standing delay has stayed above target for a
// full interval — and there are callers waiting. It is always false when CoDel is
// disabled. While overloaded the bulkhead sheds stale callers and serves
// newest-first (see [BulkheadCoDel]).
func (b *Bulkhead) Overloaded() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return len(b.waiters) > 0 && b.codel.isOverloaded()
}

// CoDelLoad reports how close the wait queue is to shedding under the
// controlled-delay discipline, in [0, 1]: the standing queue delay (the dwell of
// the oldest waiter) as a fraction of the slough timeout (2 × target). It is 0
// when CoDel is disabled or the queue is empty, and saturates at 1 once the
// oldest waiter is at or past the slough timeout. See [BulkheadCoDel].
func (b *Bulkhead) CoDelLoad() float64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.waiters) == 0 || !b.codel.enabled() {
		return 0
	}

	slough := b.codel.sloughTimeout()
	standing := b.clock.Now().Sub(b.waiters[0].enqueued)

	return clampUnitInterval(float64(standing) / float64(slough))
}
