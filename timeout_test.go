package r8e_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Tests: Function completes before timeout -> return result
// ---------------------------------------------------------------------------

func TestDoTimeoutSuccessBeforeDeadline(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}

	result, err := r8e.DoTimeout[string](
		context.Background(),
		time.Second,
		func(_ context.Context) (string, error) {
			return "hello", nil
		},
		hooks,
	)
	require.NoError(t, err)
	require.Equal(t, "hello", result)
}

// ---------------------------------------------------------------------------
// Tests: Function completes before timeout with error -> return error
// ---------------------------------------------------------------------------

func TestDoTimeoutFnErrorBeforeDeadline(t *testing.T) {
	t.Parallel()

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

	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 0, result)
}

// ---------------------------------------------------------------------------
// Tests: Function exceeds timeout -> r8e.ErrTimeout
// ---------------------------------------------------------------------------

func TestDoTimeoutExceedsDeadline(t *testing.T) {
	t.Parallel()

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

	require.ErrorIs(t, err, r8e.ErrTimeout)
	// Zero-value should be returned on timeout.
	require.Equal(t, "", result)
}

// ---------------------------------------------------------------------------
// Tests: Parent context already cancelled -> context error
// ---------------------------------------------------------------------------

func TestDoTimeoutParentContextAlreadyCancelled(t *testing.T) {
	t.Parallel()

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

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 0, result)
}

// ---------------------------------------------------------------------------
// Tests: Parent context cancelled during execution -> parent context error
// ---------------------------------------------------------------------------

func TestDoTimeoutParentContextCancelledDuringExecution(t *testing.T) {
	t.Parallel()

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

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "", result)
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook fired on timeout
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookFired(t *testing.T) {
	t.Parallel()

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

	require.True(t, hookCalled.Load())
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook NOT fired on success
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookNotFiredOnSuccess(t *testing.T) {
	t.Parallel()

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
	require.NoError(t, err)
	require.False(t, hookCalled.Load())
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook NOT fired on fn error (non-timeout)
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookNotFiredOnFnError(t *testing.T) {
	t.Parallel()

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

	require.False(t, hookCalled.Load())
}

// ---------------------------------------------------------------------------
// Tests: Zero-value result returned on timeout (for typed generic)
// ---------------------------------------------------------------------------

func TestDoTimeoutZeroValueOnTimeoutInt(t *testing.T) {
	t.Parallel()

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

	require.ErrorIs(t, err, r8e.ErrTimeout)
	require.Equal(t, 0, result)
}

func TestDoTimeoutZeroValueOnTimeoutStruct(t *testing.T) {
	t.Parallel()

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

	require.ErrorIs(t, err, r8e.ErrTimeout)
	require.Equal(t, payload{}, result)
}

// ---------------------------------------------------------------------------
// Tests: Nil hooks do not panic
// ---------------------------------------------------------------------------

func TestDoTimeoutNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()

	var hooks *r8e.Hooks // nil *Hooks must be a no-op

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
	t.Parallel()

	// Deterministic virtual time: the 10ms of work resolves before the 500ms
	// deadline without any real waiting.
	synctest.Test(t, func(t *testing.T) {
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
		require.NoError(t, err)
		require.Equal(t, "slow-ok", result)
	})
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook NOT fired on parent context cancellation
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookNotFiredOnParentCancel(t *testing.T) {
	t.Parallel()

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

	require.False(t, hookCalled.Load())
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
