package r8e_test

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/byte4ever/r8e"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Acquire under limit succeeds
// ---------------------------------------------------------------------------

func TestBulkheadAcquireUnderLimit(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(3, &r8e.Hooks{})

	require.NoError(t, bh.Acquire())
	require.NoError(t, bh.Acquire())
	require.NoError(t, bh.Acquire())
}

// ---------------------------------------------------------------------------
// Acquire at limit returns ErrBulkheadFull
// ---------------------------------------------------------------------------

func TestBulkheadAcquireAtLimitReturnsErrBulkheadFull(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(2, &r8e.Hooks{})

	// Fill up both slots.
	require.NoError(t, bh.Acquire())
	require.NoError(t, bh.Acquire())

	// Third acquire should fail.
	require.ErrorIs(t, bh.Acquire(), r8e.ErrBulkheadFull)
}

// ---------------------------------------------------------------------------
// Release frees a slot (can acquire again)
// ---------------------------------------------------------------------------

func TestBulkheadReleaseFreesSlot(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(1, &r8e.Hooks{})

	require.NoError(t, bh.Acquire())

	// At capacity — should fail.
	require.ErrorIs(t, bh.Acquire(), r8e.ErrBulkheadFull)

	// Release and try again.
	bh.Release()

	require.NoError(t, bh.Acquire())
}

// ---------------------------------------------------------------------------
// Full() returns correct state
// ---------------------------------------------------------------------------

func TestBulkheadFullReturnsCorrectState(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(2, &r8e.Hooks{})

	require.False(t, bh.Full())

	bh.Acquire()
	require.False(t, bh.Full())

	bh.Acquire()
	require.True(t, bh.Full())

	bh.Release()
	require.False(t, bh.Full())
}

// ---------------------------------------------------------------------------
// Concurrent acquire/release (100 goroutines)
// ---------------------------------------------------------------------------

func TestBulkheadConcurrentAccess(t *testing.T) {
	t.Parallel()

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
	require.False(t, bh.Full())
}

// ---------------------------------------------------------------------------
// Hook emissions: Acquired, Full, Released
// ---------------------------------------------------------------------------

func TestBulkheadHookEmissions(t *testing.T) {
	t.Parallel()

	var acquiredCount, fullCount, releasedCount atomic.Int64
	hooks := &r8e.Hooks{
		OnBulkheadAcquired: func() { acquiredCount.Add(1) },
		OnBulkheadFull:     func() { fullCount.Add(1) },
		OnBulkheadReleased: func() { releasedCount.Add(1) },
	}

	bh := r8e.NewBulkhead(1, hooks)

	// Acquire — should fire Acquired hook.
	bh.Acquire()
	require.Equal(t, int64(1), acquiredCount.Load())

	// Acquire at capacity — should fire Full hook.
	bh.Acquire()
	require.Equal(t, int64(1), fullCount.Load())

	// Release — should fire Released hook.
	bh.Release()
	require.Equal(t, int64(1), releasedCount.Load())
}

// ---------------------------------------------------------------------------
// Multiple sequential acquire/release cycles
// ---------------------------------------------------------------------------

func TestBulkheadMultipleSequentialCycles(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(1, &r8e.Hooks{})

	for i := range 10 {
		require.NoErrorf(t, bh.Acquire(), "cycle %d", i)
		require.Truef(t, bh.Full(), "cycle %d", i)
		require.ErrorIsf(t, bh.Acquire(), r8e.ErrBulkheadFull, "cycle %d", i)
		bh.Release()
		require.Falsef(t, bh.Full(), "cycle %d", i)
	}
}

// ---------------------------------------------------------------------------
// Nil hooks don't panic
// ---------------------------------------------------------------------------

func TestBulkheadNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(1, &r8e.Hooks{})

	bh.Acquire()
	bh.Release()
	bh.Full()
}

// ---------------------------------------------------------------------------
// Single slot bulkhead (edge case)
// ---------------------------------------------------------------------------

func TestBulkheadSingleSlot(t *testing.T) {
	t.Parallel()

	bh := r8e.NewBulkhead(1, &r8e.Hooks{})

	require.NoError(t, bh.Acquire())
	require.True(t, bh.Full())

	require.ErrorIs(t, bh.Acquire(), r8e.ErrBulkheadFull)

	bh.Release()
	require.False(t, bh.Full())
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
