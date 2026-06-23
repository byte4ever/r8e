package r8e

import "time"

// codel implements the RFC 8289 / Facebook folly "controlled delay" overload
// test that disciplines the bulkhead wait queue (see [BulkheadCoDel]).
//
// Pattern: Controlled Delay (CoDel) — overload detection by the standing queue
// delay, the same kind of internal algorithm controller as adaptiveTimeout and
// adaptiveHedge are for their windows.
//
// It watches the standing queue delay — the dwell of the oldest queued waiter —
// sampled at each dequeue event. RFC 8289's "first above target" rule then
// decides overload: once the standing delay has stayed above target for a full
// interval the queue is declared overloaded; a single sample at or below target
// clears it immediately. While overloaded the bulkhead sheds waiters past the
// slough timeout (2 × target) and serves newest-first (adaptive LIFO); while
// healthy it serves oldest-first (FIFO) and never sheds. A zero target disables
// the discipline, leaving the plain max-wait bulkhead.
//
// codel holds no lock of its own: every field is read and written under the
// owning [Bulkhead]'s mutex, so it is only ever touched single-threaded.
type codel struct {
	// aboveSince is when the standing delay first rose above target; the zero
	// time means the last sample was at or below target. It carries a pointer
	// (time.Time.loc), so it leads the struct for field alignment.
	aboveSince time.Time

	target   time.Duration // acceptable standing queue delay (folly default 5ms)
	interval time.Duration // window the delay must persist above target (default 100ms)

	overloaded bool // latched: standing delay stayed above target a full interval
}

// enabled reports whether the controlled-delay discipline is active. A
// non-positive target (the default) leaves the bulkhead a plain max-wait queue.
func (c *codel) enabled() bool { return c.target > 0 }

// isOverloaded reports whether the queue is currently in the overloaded regime.
// It is the single guarded reader of the latch: the overloaded flag is only ever
// set while enabled, but conjoining enabled() here keeps that invariant enforced
// in one place rather than relied on at every call site.
func (c *codel) isOverloaded() bool { return c.overloaded && c.enabled() }

// sloughTimeout is the per-waiter dwell past which an overloaded queue sheds a
// caller — twice the target standing delay, matching folly's getSloughTimeout.
func (c *codel) sloughTimeout() time.Duration { return 2 * c.target }

// observe folds the current standing queue delay into the overload latch using
// RFC 8289's first-above-target test, with now the sampling instant. A delay at
// or below target marks the queue healthy at once; a delay above target arms a
// timer (aboveSince) and latches overload only once that delay has persisted for
// a full interval.
func (c *codel) observe(standing time.Duration, now time.Time) {
	if standing <= c.target {
		c.aboveSince = time.Time{}
		c.overloaded = false

		return
	}

	if c.aboveSince.IsZero() {
		c.aboveSince = now

		return
	}

	if now.Sub(c.aboveSince) >= c.interval {
		c.overloaded = true
	}
}

// reconfigure adopts a new target/interval. Changing either resets the latch so
// the new thresholds start from a clean interval rather than inheriting a stale
// above-target timer; an unchanged pair is a no-op that preserves the latch.
func (c *codel) reconfigure(target, interval time.Duration) {
	if target == c.target && interval == c.interval {
		return
	}

	c.target = target
	c.interval = interval
	c.aboveSince = time.Time{}
	c.overloaded = false
}
