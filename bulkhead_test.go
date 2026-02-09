package r8e_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/byte4ever/r8e"
)

// ---------------------------------------------------------------------------
// Acquire under limit succeeds
// ---------------------------------------------------------------------------

func TestBulkheadAcquireUnderLimit(t *testing.T) {
	bh := r8e.NewBulkhead(3, &r8e.Hooks{})

	if err := bh.Acquire(); err != nil {
		t.Fatalf("Acquire() = %v, want nil (1st slot)", err)
	}
	if err := bh.Acquire(); err != nil {
		t.Fatalf("Acquire() = %v, want nil (2nd slot)", err)
	}
	if err := bh.Acquire(); err != nil {
		t.Fatalf("Acquire() = %v, want nil (3rd slot)", err)
	}
}

// ---------------------------------------------------------------------------
// Acquire at limit returns ErrBulkheadFull
// ---------------------------------------------------------------------------

func TestBulkheadAcquireAtLimitReturnsErrBulkheadFull(t *testing.T) {
	bh := r8e.NewBulkhead(2, &r8e.Hooks{})

	// Fill up both slots.
	if err := bh.Acquire(); err != nil {
		t.Fatalf("Acquire() = %v, want nil", err)
	}
	if err := bh.Acquire(); err != nil {
		t.Fatalf("Acquire() = %v, want nil", err)
	}

	// Third acquire should fail.
	if err := bh.Acquire(); err != r8e.ErrBulkheadFull {
		t.Fatalf("Acquire() = %v, want ErrBulkheadFull", err)
	}
}

// ---------------------------------------------------------------------------
// Release frees a slot (can acquire again)
// ---------------------------------------------------------------------------

func TestBulkheadReleaseFreesSlot(t *testing.T) {
	bh := r8e.NewBulkhead(1, &r8e.Hooks{})

	if err := bh.Acquire(); err != nil {
		t.Fatalf("Acquire() = %v, want nil", err)
	}

	// At capacity — should fail.
	if err := bh.Acquire(); err != r8e.ErrBulkheadFull {
		t.Fatalf("Acquire() at capacity = %v, want ErrBulkheadFull", err)
	}

	// Release and try again.
	bh.Release()

	if err := bh.Acquire(); err != nil {
		t.Fatalf("Acquire() after Release() = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// Full() returns correct state
// ---------------------------------------------------------------------------

func TestBulkheadFullReturnsCorrectState(t *testing.T) {
	bh := r8e.NewBulkhead(2, &r8e.Hooks{})

	if bh.Full() {
		t.Fatal("Full() = true on fresh bulkhead, want false")
	}

	bh.Acquire()
	if bh.Full() {
		t.Fatal("Full() = true after 1 acquire (max 2), want false")
	}

	bh.Acquire()
	if !bh.Full() {
		t.Fatal("Full() = false at capacity, want true")
	}

	bh.Release()
	if bh.Full() {
		t.Fatal("Full() = true after release, want false")
	}
}

// ---------------------------------------------------------------------------
// Concurrent acquire/release (100 goroutines)
// ---------------------------------------------------------------------------

func TestBulkheadConcurrentAccess(t *testing.T) {
	const maxConcurrent = 10
	const goroutines = 100

	bh := r8e.NewBulkhead(maxConcurrent, &r8e.Hooks{})

	var wg sync.WaitGroup
	wg.Add(goroutines)

	var fullCount atomic.Int64

	for range goroutines {
		go func() {
			defer wg.Done()

			if err := bh.Acquire(); err != nil {
				fullCount.Add(1)
				return
			}
			// Simulate work — no sleep needed, just release.
			_ = bh.Full()
			bh.Release()
		}()
	}

	wg.Wait()

	// After all goroutines finish, bulkhead should be empty.
	if bh.Full() {
		t.Fatal("Full() = true after all goroutines completed, want false")
	}
}

// ---------------------------------------------------------------------------
// Hook emissions: Acquired, Full, Released
// ---------------------------------------------------------------------------

func TestBulkheadHookEmissions(t *testing.T) {
	var acquiredCount, fullCount, releasedCount atomic.Int64
	hooks := &r8e.Hooks{
		OnBulkheadAcquired: func() { acquiredCount.Add(1) },
		OnBulkheadFull:     func() { fullCount.Add(1) },
		OnBulkheadReleased: func() { releasedCount.Add(1) },
	}

	bh := r8e.NewBulkhead(1, hooks)

	// Acquire — should fire Acquired hook.
	bh.Acquire()
	if got := acquiredCount.Load(); got != 1 {
		t.Fatalf("OnBulkheadAcquired called %d times, want 1", got)
	}

	// Acquire at capacity — should fire Full hook.
	bh.Acquire()
	if got := fullCount.Load(); got != 1 {
		t.Fatalf("OnBulkheadFull called %d times, want 1", got)
	}

	// Release — should fire Released hook.
	bh.Release()
	if got := releasedCount.Load(); got != 1 {
		t.Fatalf("OnBulkheadReleased called %d times, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Multiple sequential acquire/release cycles
// ---------------------------------------------------------------------------

func TestBulkheadMultipleSequentialCycles(t *testing.T) {
	bh := r8e.NewBulkhead(1, &r8e.Hooks{})

	for i := range 10 {
		if err := bh.Acquire(); err != nil {
			t.Fatalf("cycle %d: Acquire() = %v, want nil", i, err)
		}
		if !bh.Full() {
			t.Fatalf("cycle %d: Full() = false at capacity, want true", i)
		}
		if err := bh.Acquire(); err != r8e.ErrBulkheadFull {
			t.Fatalf(
				"cycle %d: Acquire() at capacity = %v, want ErrBulkheadFull",
				i,
				err,
			)
		}
		bh.Release()
		if bh.Full() {
			t.Fatalf("cycle %d: Full() = true after release, want false", i)
		}
	}
}

// ---------------------------------------------------------------------------
// Nil hooks don't panic
// ---------------------------------------------------------------------------

func TestBulkheadNilHooksDoNotPanic(t *testing.T) {
	bh := r8e.NewBulkhead(1, &r8e.Hooks{})

	bh.Acquire()
	bh.Release()
	bh.Full()
}

// ---------------------------------------------------------------------------
// Single slot bulkhead (edge case)
// ---------------------------------------------------------------------------

func TestBulkheadSingleSlot(t *testing.T) {
	bh := r8e.NewBulkhead(1, &r8e.Hooks{})

	if err := bh.Acquire(); err != nil {
		t.Fatalf("Acquire() = %v, want nil", err)
	}
	if !bh.Full() {
		t.Fatal("Full() = false, want true")
	}

	err := bh.Acquire()
	if err != r8e.ErrBulkheadFull {
		t.Fatalf("Acquire() = %v, want ErrBulkheadFull", err)
	}

	bh.Release()
	if bh.Full() {
		t.Fatal("Full() = true after release, want false")
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkBulkheadAcquireRelease(b *testing.B) {
	bh := r8e.NewBulkhead(1000, &r8e.Hooks{})

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			if err := bh.Acquire(); err == nil {
				bh.Release()
			}
		}
	})
}
