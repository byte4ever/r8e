package r8e_test

import (
	"context"
	"errors"
	"testing"

	"github.com/byte4ever/r8e"
)

// ---------------------------------------------------------------------------
// DoFallback: Success passes through
// ---------------------------------------------------------------------------

func TestDoFallbackSuccessPassesThrough(t *testing.T) {
	hooks := &r8e.Hooks{}

	result, err := r8e.DoFallback[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		"fallback-value",
		hooks,
	)
	if err != nil {
		t.Fatalf("DoFallback() error = %v, want nil", err)
	}
	if result != "ok" {
		t.Fatalf("DoFallback() = %q, want %q", result, "ok")
	}
}

// ---------------------------------------------------------------------------
// DoFallback: Error triggers static fallback
// ---------------------------------------------------------------------------

func TestDoFallbackErrorTriggersStaticFallback(t *testing.T) {
	hooks := &r8e.Hooks{}

	result, err := r8e.DoFallback[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("boom")
		},
		"safe-default",
		hooks,
	)
	if err != nil {
		t.Fatalf("DoFallback() error = %v, want nil", err)
	}
	if result != "safe-default" {
		t.Fatalf("DoFallback() = %q, want %q", result, "safe-default")
	}
}

// ---------------------------------------------------------------------------
// DoFallbackFunc: Success passes through
// ---------------------------------------------------------------------------

func TestDoFallbackFuncSuccessPassesThrough(t *testing.T) {
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
	if err != nil {
		t.Fatalf("DoFallbackFunc() error = %v, want nil", err)
	}
	if result != "ok" {
		t.Fatalf("DoFallbackFunc() = %q, want %q", result, "ok")
	}
}

// ---------------------------------------------------------------------------
// DoFallbackFunc: Error triggers function fallback
// ---------------------------------------------------------------------------

func TestDoFallbackFuncErrorTriggersFunctionFallback(t *testing.T) {
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
	if err != nil {
		t.Fatalf("DoFallbackFunc() error = %v, want nil", err)
	}
	if result != "recovered-from-boom" {
		t.Fatalf(
			"DoFallbackFunc() = %q, want %q",
			result,
			"recovered-from-boom",
		)
	}
}

// ---------------------------------------------------------------------------
// DoFallbackFunc: Fallback function can itself return error
// ---------------------------------------------------------------------------

func TestDoFallbackFuncFallbackCanReturnError(t *testing.T) {
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

	if !errors.Is(err, fallbackErr) {
		t.Fatalf("DoFallbackFunc() error = %v, want %v", err, fallbackErr)
	}
	if result != -1 {
		t.Fatalf("DoFallbackFunc() = %d, want -1", result)
	}
}

// ---------------------------------------------------------------------------
// OnFallbackUsed hook fires with original error (DoFallback)
// ---------------------------------------------------------------------------

func TestDoFallbackOnFallbackUsedHookFires(t *testing.T) {
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

	if !errors.Is(hookErr, origErr) {
		t.Fatalf(
			"OnFallbackUsed hook received error = %v, want %v",
			hookErr,
			origErr,
		)
	}
}

// ---------------------------------------------------------------------------
// OnFallbackUsed hook fires with original error (DoFallbackFunc)
// ---------------------------------------------------------------------------

func TestDoFallbackFuncOnFallbackUsedHookFires(t *testing.T) {
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

	if !errors.Is(hookErr, origErr) {
		t.Fatalf(
			"OnFallbackUsed hook received error = %v, want %v",
			hookErr,
			origErr,
		)
	}
}

// ---------------------------------------------------------------------------
// Hook NOT fired on success (DoFallback)
// ---------------------------------------------------------------------------

func TestDoFallbackHookNotFiredOnSuccess(t *testing.T) {
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
	if err != nil {
		t.Fatalf("DoFallback() error = %v, want nil", err)
	}
	if hookCalled {
		t.Fatal("OnFallbackUsed hook should not fire on success")
	}
}

// ---------------------------------------------------------------------------
// Hook NOT fired on success (DoFallbackFunc)
// ---------------------------------------------------------------------------

func TestDoFallbackFuncHookNotFiredOnSuccess(t *testing.T) {
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
	if err != nil {
		t.Fatalf("DoFallbackFunc() error = %v, want nil", err)
	}
	if hookCalled {
		t.Fatal("OnFallbackUsed hook should not fire on success")
	}
}

// ---------------------------------------------------------------------------
// Nil hooks don't panic (DoFallback)
// ---------------------------------------------------------------------------

func TestDoFallbackNilHooksDoNotPanic(t *testing.T) {
	hooks := &r8e.Hooks{} // all fields nil

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
	hooks := &r8e.Hooks{} // all fields nil

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
