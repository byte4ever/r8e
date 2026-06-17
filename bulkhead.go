package r8e

import "sync/atomic"

// Bulkhead limits concurrent access to a resource.
//
// Pattern: Bulkhead — semaphore-based concurrency limiter prevents
// resource exhaustion; lock-free via atomic CAS for slot acquisition.
type Bulkhead struct {
	hooks         *Hooks
	maxConcurrent int64
	current       atomic.Int64
}

// NewBulkhead creates a bulkhead that allows at most maxConcurrent simultaneous
// calls.
func NewBulkhead(maxConcurrent int, hooks *Hooks) *Bulkhead {
	return &Bulkhead{
		maxConcurrent: int64(maxConcurrent),
		hooks:         hooks,
	}
}

// Acquire attempts to acquire a slot. Returns ErrBulkheadFull if at capacity.
func (b *Bulkhead) Acquire() error {
	for {
		cur := b.current.Load()
		if cur >= b.maxConcurrent {
			b.hooks.emitBulkheadFull()
			return ErrBulkheadFull
		}

		if b.current.CompareAndSwap(cur, cur+1) {
			b.hooks.emitBulkheadAcquired()
			return nil
		}
	}
}

// Release releases a slot previously taken by a successful [Bulkhead.Acquire].
// A Release with no matching Acquire (or a double Release) is a no-op rather
// than driving the counter negative, which would silently disable the limiter.
func (b *Bulkhead) Release() {
	for {
		cur := b.current.Load()
		if cur <= 0 {
			return
		}

		if b.current.CompareAndSwap(cur, cur-1) {
			b.hooks.emitBulkheadReleased()
			return
		}
	}
}

// Full returns true if all slots are in use.
func (b *Bulkhead) Full() bool {
	return b.current.Load() >= b.maxConcurrent
}
