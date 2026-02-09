package r8e

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

// ---------------------------------------------------------------------------
// Each hook is called when set and emitted
// ---------------------------------------------------------------------------

func TestEmitRetryCallsHook(t *testing.T) {
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

	if gotAttempt != 3 {
		t.Fatalf("OnRetry attempt = %d, want 3", gotAttempt)
	}
	if gotErr != cause {
		t.Fatalf("OnRetry err = %v, want %v", gotErr, cause)
	}
}

func TestEmitCircuitOpenCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnCircuitOpen: func() { called = true }}
	h.emitCircuitOpen()
	if !called {
		t.Fatal("OnCircuitOpen not called")
	}
}

func TestEmitCircuitCloseCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnCircuitClose: func() { called = true }}
	h.emitCircuitClose()
	if !called {
		t.Fatal("OnCircuitClose not called")
	}
}

func TestEmitCircuitHalfOpenCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnCircuitHalfOpen: func() { called = true }}
	h.emitCircuitHalfOpen()
	if !called {
		t.Fatal("OnCircuitHalfOpen not called")
	}
}

func TestEmitRateLimitedCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnRateLimited: func() { called = true }}
	h.emitRateLimited()
	if !called {
		t.Fatal("OnRateLimited not called")
	}
}

func TestEmitBulkheadFullCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnBulkheadFull: func() { called = true }}
	h.emitBulkheadFull()
	if !called {
		t.Fatal("OnBulkheadFull not called")
	}
}

func TestEmitBulkheadAcquiredCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnBulkheadAcquired: func() { called = true }}
	h.emitBulkheadAcquired()
	if !called {
		t.Fatal("OnBulkheadAcquired not called")
	}
}

func TestEmitBulkheadReleasedCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnBulkheadReleased: func() { called = true }}
	h.emitBulkheadReleased()
	if !called {
		t.Fatal("OnBulkheadReleased not called")
	}
}

func TestEmitTimeoutCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnTimeout: func() { called = true }}
	h.emitTimeout()
	if !called {
		t.Fatal("OnTimeout not called")
	}
}

func TestEmitHedgeTriggeredCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnHedgeTriggered: func() { called = true }}
	h.emitHedgeTriggered()
	if !called {
		t.Fatal("OnHedgeTriggered not called")
	}
}

func TestEmitHedgeWonCallsHook(t *testing.T) {
	called := false
	h := Hooks{OnHedgeWon: func() { called = true }}
	h.emitHedgeWon()
	if !called {
		t.Fatal("OnHedgeWon not called")
	}
}

func TestEmitFallbackUsedCallsHook(t *testing.T) {
	var gotErr error
	h := Hooks{
		OnFallbackUsed: func(err error) { gotErr = err },
	}
	cause := errors.New("primary failed")
	h.emitFallbackUsed(cause)
	if gotErr != cause {
		t.Fatalf("OnFallbackUsed err = %v, want %v", gotErr, cause)
	}
}

// ---------------------------------------------------------------------------
// All nil hooks don't panic when emitted
// ---------------------------------------------------------------------------

func TestNilHooksDoNotPanic(t *testing.T) {
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
	if got := count.Load(); got != want {
		t.Fatalf("total hook calls = %d, want %d", got, want)
	}
}
