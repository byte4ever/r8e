package r8e

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Each hook is called when set and emitted
// ---------------------------------------------------------------------------

func TestEmitRetryCallsHook(t *testing.T) {
	t.Parallel()

	var gotAttempt int
	var gotErr error
	h := Hooks{
		OnRetry: func(attempt int, err error) {
			gotAttempt = attempt
			gotErr = err
		},
	}
	cause := errors.New("retry me")
	h.emitRetry(3, cause)

	require.Equal(t, 3, gotAttempt)
	require.Equal(t, cause, gotErr)
}

func TestEmitCircuitOpenCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnCircuitOpen: func() { called = true }}
	h.emitCircuitOpen()
	require.True(t, called)
}

func TestEmitCircuitCloseCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnCircuitClose: func() { called = true }}
	h.emitCircuitClose()
	require.True(t, called)
}

func TestEmitCircuitHalfOpenCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnCircuitHalfOpen: func() { called = true }}
	h.emitCircuitHalfOpen()
	require.True(t, called)
}

func TestEmitRateLimitedCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnRateLimited: func() { called = true }}
	h.emitRateLimited()
	require.True(t, called)
}

func TestEmitBulkheadFullCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnBulkheadFull: func() { called = true }}
	h.emitBulkheadFull()
	require.True(t, called)
}

func TestEmitBulkheadAcquiredCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnBulkheadAcquired: func() { called = true }}
	h.emitBulkheadAcquired()
	require.True(t, called)
}

func TestEmitBulkheadReleasedCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnBulkheadReleased: func() { called = true }}
	h.emitBulkheadReleased()
	require.True(t, called)
}

func TestEmitTimeoutCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnTimeout: func() { called = true }}
	h.emitTimeout()
	require.True(t, called)
}

func TestEmitHedgeTriggeredCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnHedgeTriggered: func() { called = true }}
	h.emitHedgeTriggered()
	require.True(t, called)
}

func TestEmitHedgeWonCallsHook(t *testing.T) {
	t.Parallel()

	called := false
	h := Hooks{OnHedgeWon: func() { called = true }}
	h.emitHedgeWon()
	require.True(t, called)
}

func TestEmitFallbackUsedCallsHook(t *testing.T) {
	t.Parallel()

	var gotErr error
	h := Hooks{
		OnFallbackUsed: func(err error) { gotErr = err },
	}
	cause := errors.New("primary failed")
	h.emitFallbackUsed(cause)
	require.Equal(t, cause, gotErr)
}

// ---------------------------------------------------------------------------
// All nil hooks don't panic when emitted
// ---------------------------------------------------------------------------

func TestNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()

	var h Hooks // all fields nil

	// None of these should panic.
	h.emitRetry(1, errors.New("err"))
	h.emitCircuitOpen()
	h.emitCircuitClose()
	h.emitCircuitHalfOpen()
	h.emitRateLimited()
	h.emitBulkheadFull()
	h.emitBulkheadAcquired()
	h.emitBulkheadReleased()
	h.emitTimeout()
	h.emitHedgeTriggered()
	h.emitHedgeWon()
	h.emitFallbackUsed(errors.New("err"))
}

// ---------------------------------------------------------------------------
// Concurrent emission is safe
// ---------------------------------------------------------------------------

func TestConcurrentEmissionIsSafe(t *testing.T) {
	t.Parallel()

	var count atomic.Int64
	h := Hooks{
		OnRetry:            func(int, error) { count.Add(1) },
		OnCircuitOpen:      func() { count.Add(1) },
		OnCircuitClose:     func() { count.Add(1) },
		OnCircuitHalfOpen:  func() { count.Add(1) },
		OnRateLimited:      func() { count.Add(1) },
		OnBulkheadFull:     func() { count.Add(1) },
		OnBulkheadAcquired: func() { count.Add(1) },
		OnBulkheadReleased: func() { count.Add(1) },
		OnTimeout:          func() { count.Add(1) },
		OnHedgeTriggered:   func() { count.Add(1) },
		OnHedgeWon:         func() { count.Add(1) },
		OnFallbackUsed:     func(error) { count.Add(1) },
	}

	const goroutines = 10
	const hooksPerGoroutine = 12

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			h.emitRetry(1, errors.New("err"))
			h.emitCircuitOpen()
			h.emitCircuitClose()
			h.emitCircuitHalfOpen()
			h.emitRateLimited()
			h.emitBulkheadFull()
			h.emitBulkheadAcquired()
			h.emitBulkheadReleased()
			h.emitTimeout()
			h.emitHedgeTriggered()
			h.emitHedgeWon()
			h.emitFallbackUsed(errors.New("err"))
		}()
	}

	wg.Wait()

	want := int64(goroutines * hooksPerGoroutine)
	require.Equal(t, want, count.Load())
}
