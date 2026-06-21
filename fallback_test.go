package r8e_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
)

// ---------------------------------------------------------------------------
// DoFallback: Success passes through
// ---------------------------------------------------------------------------

func TestDoFallbackSuccessPassesThrough(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}

	result, err := r8e.DoFallback[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		"fallback-value",
		hooks,
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

// ---------------------------------------------------------------------------
// DoFallback: Error triggers static fallback
// ---------------------------------------------------------------------------

func TestDoFallbackErrorTriggersStaticFallback(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}

	result, err := r8e.DoFallback[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("boom")
		},
		"safe-default",
		hooks,
	)
	require.NoError(t, err)
	require.Equal(t, "safe-default", result)
}

// ---------------------------------------------------------------------------
// DoFallbackFunc: Success passes through
// ---------------------------------------------------------------------------

func TestDoFallbackFuncSuccessPassesThrough(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}

	result, err := r8e.DoFallbackFunc[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		func(_ error) (string, error) {
			return "should-not-reach", nil
		},
		hooks,
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

// ---------------------------------------------------------------------------
// DoFallbackFunc: Error triggers function fallback
// ---------------------------------------------------------------------------

func TestDoFallbackFuncErrorTriggersFunctionFallback(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}

	result, err := r8e.DoFallbackFunc[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("boom")
		},
		func(origErr error) (string, error) {
			return "recovered-from-" + origErr.Error(), nil
		},
		hooks,
	)
	require.NoError(t, err)
	require.Equal(t, "recovered-from-boom", result)
}

// ---------------------------------------------------------------------------
// DoFallbackFunc: Fallback function can itself return error
// ---------------------------------------------------------------------------

func TestDoFallbackFuncFallbackCanReturnError(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}
	fallbackErr := errors.New("fallback also failed")

	result, err := r8e.DoFallbackFunc[int](
		context.Background(),
		func(_ context.Context) (int, error) {
			return 0, errors.New("primary failed")
		},
		func(_ error) (int, error) {
			return -1, fallbackErr
		},
		hooks,
	)

	require.ErrorIs(t, err, fallbackErr)
	require.Equal(t, -1, result)
}

// ---------------------------------------------------------------------------
// OnFallbackUsed hook fires with original error (DoFallback)
// ---------------------------------------------------------------------------

func TestDoFallbackOnFallbackUsedHookFires(t *testing.T) {
	t.Parallel()

	origErr := errors.New("original error")
	var hookErr error
	hooks := &r8e.Hooks{
		OnFallbackUsed: func(err error) {
			hookErr = err
		},
	}

	_, _ = r8e.DoFallback[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", origErr
		},
		"default",
		hooks,
	)

	require.ErrorIs(t, hookErr, origErr)
}

// ---------------------------------------------------------------------------
// OnFallbackUsed hook fires with original error (DoFallbackFunc)
// ---------------------------------------------------------------------------

func TestDoFallbackFuncOnFallbackUsedHookFires(t *testing.T) {
	t.Parallel()

	origErr := errors.New("original error")
	var hookErr error
	hooks := &r8e.Hooks{
		OnFallbackUsed: func(err error) {
			hookErr = err
		},
	}

	_, _ = r8e.DoFallbackFunc[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", origErr
		},
		func(_ error) (string, error) {
			return "recovered", nil
		},
		hooks,
	)

	require.ErrorIs(t, hookErr, origErr)
}

// ---------------------------------------------------------------------------
// Hook NOT fired on success (DoFallback)
// ---------------------------------------------------------------------------

func TestDoFallbackHookNotFiredOnSuccess(t *testing.T) {
	t.Parallel()

	hookCalled := false
	hooks := &r8e.Hooks{
		OnFallbackUsed: func(_ error) {
			hookCalled = true
		},
	}

	_, err := r8e.DoFallback[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		"default",
		hooks,
	)
	require.NoError(t, err)
	assert.False(t, hookCalled, "OnFallbackUsed hook should not fire on success")
}

// ---------------------------------------------------------------------------
// Hook NOT fired on success (DoFallbackFunc)
// ---------------------------------------------------------------------------

func TestDoFallbackFuncHookNotFiredOnSuccess(t *testing.T) {
	t.Parallel()

	hookCalled := false
	hooks := &r8e.Hooks{
		OnFallbackUsed: func(_ error) {
			hookCalled = true
		},
	}

	_, err := r8e.DoFallbackFunc[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		func(_ error) (string, error) {
			return "nope", nil
		},
		hooks,
	)
	require.NoError(t, err)
	assert.False(t, hookCalled, "OnFallbackUsed hook should not fire on success")
}

// ---------------------------------------------------------------------------
// Nil hooks don't panic (DoFallback)
// ---------------------------------------------------------------------------

func TestDoFallbackNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()

	var hooks *r8e.Hooks // nil *Hooks must be a no-op

	// Success path with nil hooks.
	_, _ = r8e.DoFallback[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		"default",
		hooks,
	)

	// Error path with nil hooks.
	_, _ = r8e.DoFallback[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("fail")
		},
		"default",
		hooks,
	)
	// If we reach here without panicking, the test passes.
}

// ---------------------------------------------------------------------------
// Nil hooks don't panic (DoFallbackFunc)
// ---------------------------------------------------------------------------

func TestDoFallbackFuncNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()

	var hooks *r8e.Hooks // nil *Hooks must be a no-op

	// Success path with nil hooks.
	_, _ = r8e.DoFallbackFunc[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		func(_ error) (string, error) {
			return "fallback", nil
		},
		hooks,
	)

	// Error path with nil hooks.
	_, _ = r8e.DoFallbackFunc[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("fail")
		},
		func(_ error) (string, error) {
			return "fallback", nil
		},
		hooks,
	)
	// If we reach here without panicking, the test passes.
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkDoFallback(b *testing.B) {
	hooks := &r8e.Hooks{}
	ctx := context.Background()

	for b.Loop() {
		_, _ = r8e.DoFallback[string](
			ctx,
			func(_ context.Context) (string, error) {
				return "ok", nil
			},
			"default",
			hooks,
		)
	}
}

func BenchmarkDoFallbackFunc(b *testing.B) {
	hooks := &r8e.Hooks{}
	ctx := context.Background()

	for b.Loop() {
		_, _ = r8e.DoFallbackFunc[string](
			ctx,
			func(_ context.Context) (string, error) {
				return "ok", nil
			},
			func(_ error) (string, error) {
				return "fallback", nil
			},
			hooks,
		)
	}
}
