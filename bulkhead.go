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
	// State (the in-use count and the wait queue) is guarded by a mutex so the
	// tuple mutates atomically; each waiter's grant channel is closed under that
	// lock so a Release and a concurrent timeout/cancel can never both claim the
	// same slot.
	Bulkhead struct {
		clock   Clock
		hooks   *Hooks
		waiters []chan struct{}

		mu       sync.Mutex
		maxConc  int
		cur      int
		maxWait  time.Duration
		maxQueue int
	}

	// BulkheadOption configures optional [Bulkhead] behaviour — the bounded FIFO
	// wait. With no options the bulkhead keeps the default reject-immediately
	// semantics.
	BulkheadOption func(*bulkheadConfig)

	bulkheadConfig struct {
		maxWait  time.Duration
		maxQueue int
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
// bulkhead's max-concurrency. Has no effect unless [BulkheadMaxWait] enables the
// wait.
func BulkheadQueueDepth(n int) BulkheadOption {
	return func(c *bulkheadConfig) {
		if n >= 1 {
			c.maxQueue = n
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
// replaced; max-wait and queue depth from opts are applied (unset options keep
// their current value); the clock is not reconfigurable. In-flight calls are
// unaffected. If the new concurrency limit opened capacity, queued waiters are
// granted slots in FIFO order.
func (b *Bulkhead) Reconfigure(maxConcurrent int, opts ...BulkheadOption) {
	b.mu.Lock()
	defer b.mu.Unlock()

	cfg := bulkheadConfig{maxWait: b.maxWait, maxQueue: b.maxQueue}
	for _, o := range opts {
		o(&cfg)
	}

	b.maxConc = maxConcurrent
	b.maxWait = cfg.maxWait
	b.maxQueue = effectiveQueueDepth(cfg.maxQueue, maxConcurrent)
	b.drainWaiters()
}

// Acquire reserves a slot, blocking up to the configured max-wait (see
// [BulkheadMaxWait]) if the bulkhead is full. It returns:
//   - nil once a slot is held (release it with [Bulkhead.Release]);
//   - [ErrBulkheadFull] if full and either no max-wait is configured or the wait
//     queue is already at its bounded depth;
//   - [ErrBulkheadTimeout] if the caller waited the full max-wait without a slot;
//   - ctx.Err() if ctx is cancelled while waiting.
func (b *Bulkhead) Acquire(ctx context.Context) error {
	b.mu.Lock()

	if b.cur < b.maxConc {
		b.cur++
		b.mu.Unlock()
		b.hooks.emitBulkheadAcquired()

		return nil
	}

	if b.maxWait <= 0 || len(b.waiters) >= b.maxQueue {
		b.mu.Unlock()
		b.hooks.emitBulkheadFull()

		return ErrBulkheadFull
	}

	ready := make(chan struct{})
	b.waiters = append(b.waiters, ready)
	maxWait := b.maxWait // capture under the lock; Reconfigure may change it
	b.mu.Unlock()
	b.hooks.emitBulkheadQueued()

	return b.waitForSlot(ctx, ready, maxWait)
}

// waitForSlot blocks until the queued caller (identified by ready) is handed a
// slot, the max-wait elapses, or ctx is cancelled. Exactly one outcome wins; the
// abandon check resolves any race with a concurrent Release. maxWait is captured
// by the caller under the lock so this runs without touching shared state until
// it re-locks via abandon/releaseSlot.
func (b *Bulkhead) waitForSlot(
	ctx context.Context,
	ready chan struct{},
	maxWait time.Duration,
) error {
	timer := b.clock.NewTimer(maxWait)
	defer timer.Stop()

	select {
	case <-ready:
		b.hooks.emitBulkheadAcquired()

		return nil

	case <-timer.C():
		if b.abandon(ready) {
			b.hooks.emitBulkheadTimeout()

			return ErrBulkheadTimeout
		}
		// Granted just as the timer fired — honour the slot.
		b.hooks.emitBulkheadAcquired()

		return nil

	case <-ctx.Done():
		if !b.abandon(ready) {
			// Granted just as ctx was cancelled — return the slot we now own
			// without an acquired/released hook pair (none was emitted for it).
			b.releaseSlot()
		}

		return ctx.Err() //nolint:wrapcheck // preserve context error identity
	}
}

// abandon removes the still-queued waiter (it timed out or was cancelled). It
// returns true if the waiter was removed, or false if a concurrent Release had
// already handed it a slot (and removed it from the queue), in which case the
// caller now owns that slot. Runs under mu, paired with Release removing the
// waiter under mu, so exactly one of abandon/Release claims it.
func (b *Bulkhead) abandon(ready chan struct{}) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, w := range b.waiters {
		if w == ready {
			b.waiters = removeWaiterAt(b.waiters, i)

			return true
		}
	}

	return false
}

// Release releases a slot previously taken by a successful [Bulkhead.Acquire]. A
// Release with no matching Acquire (or a double Release) is a no-op rather than
// driving the counter negative, which would silently disable the limiter. If
// callers are queued, the freed slot is handed to the head of the queue instead
// of being returned to the pool.
func (b *Bulkhead) Release() {
	if b.releaseSlot() {
		b.hooks.emitBulkheadReleased()
	}
}

// releaseSlot returns one held slot: it hands the slot to the head of the wait
// queue (FIFO) if anyone is waiting, otherwise decrements the in-use count. It
// reports whether a slot was actually released (handed off or decremented) so a
// spurious Release stays a silent no-op. The grant channel is closed under mu so
// a concurrent timeout/cancel observes the grant atomically.
func (b *Bulkhead) releaseSlot() bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.waiters) > 0 {
		b.popWaiter()

		return true
	}

	if b.cur > 0 {
		b.cur--

		return true
	}

	return false
}

// popWaiter removes the head waiter and signals its grant by closing its channel.
// Caller must hold mu and ensure the queue is non-empty.
func (b *Bulkhead) popWaiter() {
	w := b.waiters[0]
	close(w)

	b.waiters = removeWaiterAt(b.waiters, 0)
}

// drainWaiters hands newly opened capacity to queued callers in FIFO order, used
// after a concurrency-limit increase. Each grant consumes a fresh slot (cur++),
// unlike a Release handoff which transfers an existing slot. Caller must hold mu.
func (b *Bulkhead) drainWaiters() {
	for b.cur < b.maxConc && len(b.waiters) > 0 {
		b.cur++

		b.popWaiter()
	}
}

// removeWaiterAt removes index i from the FIFO waiter slice, preserving order and
// niling the freed tail slot so the dropped channel can be garbage collected.
func removeWaiterAt(waiters []chan struct{}, i int) []chan struct{} {
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
// max-wait is configured.
func (b *Bulkhead) Queued() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()

	return int64(len(b.waiters))
}
