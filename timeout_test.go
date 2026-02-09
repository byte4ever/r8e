package r8e_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
)

// ---------------------------------------------------------------------------
// Tests: Function completes before timeout -> return result
// ---------------------------------------------------------------------------

func TestDoTimeoutSuccessBeforeDeadline(t *testing.T) {
	hooks := &r8e.Hooks{}

	result, err := r8e.DoTimeout[string](
		context.Background(),
		time.Second,
		func(_ context.Context) (string, error) {
			return "hello", nil
		},
		hooks,
	)
	if err != nil {
		t.Fatalf("DoTimeout() error = %v, want nil", err)
	}
	if result != "hello" {
		t.Fatalf("DoTimeout() = %q, want %q", result, "hello")
	}
}

// ---------------------------------------------------------------------------
// Tests: Function completes before timeout with error -> return error
// ---------------------------------------------------------------------------

func TestDoTimeoutFnErrorBeforeDeadline(t *testing.T) {
	hooks := &r8e.Hooks{}
	sentinel := errors.New("application error")

	result, err := r8e.DoTimeout[int](
		context.Background(),
		time.Second,
		func(_ context.Context) (int, error) {
			return 0, sentinel
		},
		hooks,
	)

	if !errors.Is(err, sentinel) {
		t.Fatalf("DoTimeout() error = %v, want %v", err, sentinel)
	}
	if result != 0 {
		t.Fatalf("DoTimeout() = %d, want 0", result)
	}
}

// ---------------------------------------------------------------------------
// Tests: Function exceeds timeout -> r8e.ErrTimeout
// ---------------------------------------------------------------------------

func TestDoTimeoutExceedsDeadline(t *testing.T) {
	hooks := &r8e.Hooks{}

	result, err := r8e.DoTimeout[string](
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) (string, error) {
			// Block until context is cancelled (timeout).
			<-ctx.Done()
			return "late", ctx.Err()
		},
		hooks,
	)

	if !errors.Is(err, r8e.ErrTimeout) {
		t.Fatalf("DoTimeout() error = %v, want r8e.ErrTimeout", err)
	}
	// Zero-value should be returned on timeout.
	if result != "" {
		t.Fatalf("DoTimeout() = %q, want zero value %q", result, "")
	}
}

// ---------------------------------------------------------------------------
// Tests: Parent context already cancelled -> context error
// ---------------------------------------------------------------------------

func TestDoTimeoutParentContextAlreadyCancelled(t *testing.T) {
	hooks := &r8e.Hooks{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result, err := r8e.DoTimeout[int](
		ctx,
		time.Second,
		func(ctx context.Context) (int, error) {
			return 42, nil
		},
		hooks,
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DoTimeout() error = %v, want context.Canceled", err)
	}
	if result != 0 {
		t.Fatalf("DoTimeout() = %d, want 0 (zero value)", result)
	}
}

// ---------------------------------------------------------------------------
// Tests: Parent context cancelled during execution -> parent context error
// ---------------------------------------------------------------------------

func TestDoTimeoutParentContextCancelledDuringExecution(t *testing.T) {
	hooks := &r8e.Hooks{}

	ctx, cancel := context.WithCancel(context.Background())

	result, err := r8e.DoTimeout[string](
		ctx,
		5*time.Second, // long timeout, parent cancels first
		func(ctx context.Context) (string, error) {
			cancel() // cancel parent
			<-ctx.Done()
			return "", ctx.Err()
		},
		hooks,
	)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DoTimeout() error = %v, want context.Canceled", err)
	}
	if result != "" {
		t.Fatalf("DoTimeout() = %q, want zero value", result)
	}
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook fired on timeout
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookFired(t *testing.T) {
	var hookCalled atomic.Bool
	hooks := &r8e.Hooks{
		OnTimeout: func() {
			hookCalled.Store(true)
		},
	}

	_, _ = r8e.DoTimeout[string](
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		hooks,
	)

	if !hookCalled.Load() {
		t.Fatal("OnTimeout hook was not called")
	}
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook NOT fired on success
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookNotFiredOnSuccess(t *testing.T) {
	var hookCalled atomic.Bool
	hooks := &r8e.Hooks{
		OnTimeout: func() {
			hookCalled.Store(true)
		},
	}

	_, err := r8e.DoTimeout[string](
		context.Background(),
		time.Second,
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		hooks,
	)
	if err != nil {
		t.Fatalf("DoTimeout() error = %v, want nil", err)
	}
	if hookCalled.Load() {
		t.Fatal("OnTimeout hook was called on success, should not be")
	}
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook NOT fired on fn error (non-timeout)
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookNotFiredOnFnError(t *testing.T) {
	var hookCalled atomic.Bool
	hooks := &r8e.Hooks{
		OnTimeout: func() {
			hookCalled.Store(true)
		},
	}

	_, _ = r8e.DoTimeout[string](
		context.Background(),
		time.Second,
		func(_ context.Context) (string, error) {
			return "", errors.New("app error")
		},
		hooks,
	)

	if hookCalled.Load() {
		t.Fatal("OnTimeout hook was called on fn error, should not be")
	}
}

// ---------------------------------------------------------------------------
// Tests: Zero-value result returned on timeout (for typed generic)
// ---------------------------------------------------------------------------

func TestDoTimeoutZeroValueOnTimeoutInt(t *testing.T) {
	hooks := &r8e.Hooks{}

	result, err := r8e.DoTimeout[int](
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 999, ctx.Err()
		},
		hooks,
	)

	if !errors.Is(err, r8e.ErrTimeout) {
		t.Fatalf("DoTimeout() error = %v, want r8e.ErrTimeout", err)
	}
	if result != 0 {
		t.Fatalf("DoTimeout() = %d, want 0 (zero value for int)", result)
	}
}

func TestDoTimeoutZeroValueOnTimeoutStruct(t *testing.T) {
	type payload struct {
		Name  string
		Count int
	}
	hooks := &r8e.Hooks{}

	result, err := r8e.DoTimeout[payload](
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) (payload, error) {
			<-ctx.Done()
			return payload{Name: "late", Count: 42}, ctx.Err()
		},
		hooks,
	)

	if !errors.Is(err, r8e.ErrTimeout) {
		t.Fatalf("DoTimeout() error = %v, want r8e.ErrTimeout", err)
	}
	if result != (payload{}) {
		t.Fatalf("DoTimeout() = %+v, want zero value", result)
	}
}

// ---------------------------------------------------------------------------
// Tests: Nil hooks do not panic
// ---------------------------------------------------------------------------

func TestDoTimeoutNilHooksDoNotPanic(t *testing.T) {
	hooks := &r8e.Hooks{} // all nil

	_, _ = r8e.DoTimeout[string](
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		hooks,
	)
	// If we get here without panicking, the test passes.
}

// ---------------------------------------------------------------------------
// Tests: Fn returns result even after slow work within deadline
// ---------------------------------------------------------------------------

func TestDoTimeoutSlowButWithinDeadline(t *testing.T) {
	hooks := &r8e.Hooks{}

	result, err := r8e.DoTimeout[string](
		context.Background(),
		500*time.Millisecond,
		func(_ context.Context) (string, error) {
			time.Sleep(10 * time.Millisecond)
			return "slow-ok", nil
		},
		hooks,
	)
	if err != nil {
		t.Fatalf("DoTimeout() error = %v, want nil", err)
	}
	if result != "slow-ok" {
		t.Fatalf("DoTimeout() = %q, want %q", result, "slow-ok")
	}
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook NOT fired on parent context cancellation
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookNotFiredOnParentCancel(t *testing.T) {
	var hookCalled atomic.Bool
	hooks := &r8e.Hooks{
		OnTimeout: func() {
			hookCalled.Store(true)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, _ = r8e.DoTimeout[string](
		ctx,
		time.Second,
		func(ctx context.Context) (string, error) {
			return "x", nil
		},
		hooks,
	)

	if hookCalled.Load() {
		t.Fatal(
			"OnTimeout hook should not fire when parent context is cancelled",
		)
	}
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkTimeout(b *testing.B) {
	hooks := &r8e.Hooks{}
	ctx := context.Background()

	for b.Loop() {
		_, _ = r8e.DoTimeout[string](
			ctx,
			time.Second,
			func(_ context.Context) (string, error) {
				return "ok", nil
			},
			hooks,
		)
	}
}
