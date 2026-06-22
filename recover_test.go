package r8e_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
)

func TestDoRecoverNoPanic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	want := "ok"
	fn := func(_ context.Context) (string, error) { return want, nil }

	got, err := r8e.DoRecover(ctx, fn, nil)

	require.NoError(t, err)
	assert.Equal(t, want, got)
}

func TestDoRecoverNoPanicError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sentinel := errors.New("downstream error")
	fn := func(_ context.Context) (string, error) { return "", sentinel }

	_, err := r8e.DoRecover(ctx, fn, nil)

	require.ErrorIs(t, err, sentinel)
}

func TestDoRecoverPanicStringValue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fn := func(_ context.Context) (string, error) { panic("something went wrong") }

	_, err := r8e.DoRecover(ctx, fn, nil)

	require.Error(t, err)

	var pe *r8e.PanicError

	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "something went wrong", pe.Value)
}

func TestDoRecoverPanicErrorValue(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	sentinel := errors.New("panic error")
	fn := func(_ context.Context) (string, error) { panic(sentinel) }

	_, err := r8e.DoRecover(ctx, fn, nil)

	var pe *r8e.PanicError

	require.ErrorAs(t, err, &pe)
	assert.Equal(t, sentinel, pe.Value)
}

func TestDoRecoverErrorIs(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fn := func(_ context.Context) (string, error) { panic("boom") }

	_, err := r8e.DoRecover(ctx, fn, nil)

	assert.True(t, errors.Is(err, r8e.ErrPanic))
}

func TestDoRecoverErrorIsOtherSentinel(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fn := func(_ context.Context) (string, error) { panic("boom") }

	_, err := r8e.DoRecover(ctx, fn, nil)

	assert.False(t, errors.Is(err, r8e.ErrTimeout), "should not match unrelated sentinel")
}

func TestDoRecoverErrorMessage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fn := func(_ context.Context) (string, error) { panic("test panic") }

	_, err := r8e.DoRecover(ctx, fn, nil)

	assert.Contains(t, err.Error(), "test panic")
	assert.Contains(t, err.Error(), "recovered panic")
}

func TestDoRecoverStackTrace(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fn := func(_ context.Context) (string, error) { panic("stack test") }

	_, err := r8e.DoRecover(ctx, fn, nil)

	var pe *r8e.PanicError

	require.ErrorAs(t, err, &pe)
	assert.NotEmpty(t, pe.Stack, "stack trace must be captured")
}

func TestDoRecoverHookFires(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fn := func(_ context.Context) (string, error) { panic("hook test") }

	var capturedValue any

	hooks := &r8e.Hooks{
		OnPanic: func(value any) { capturedValue = value },
	}

	_, err := r8e.DoRecover(ctx, fn, hooks)

	require.ErrorIs(t, err, r8e.ErrPanic)
	assert.Equal(t, "hook test", capturedValue)
}

func TestDoRecoverHookNilSafe(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fn := func(_ context.Context) (string, error) { panic("no hook") }

	assert.NotPanics(t, func() {
		_, _ = r8e.DoRecover(ctx, fn, &r8e.Hooks{}) //nolint:errcheck // deliberately ignored
	})
}

func TestDoRecoverPanicNil(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// In Go 1.21+, panic(nil) causes recover() to return *runtime.PanicNilError
	// (a non-nil value), so it is correctly caught by the nil check.
	fn := func(_ context.Context) (string, error) { panic(nil) } //nolint:govet // intentional nil panic for test

	_, err := r8e.DoRecover(ctx, fn, nil)

	require.ErrorIs(t, err, r8e.ErrPanic)
}

func TestDoRecoverZeroValueOnPanic(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fn := func(_ context.Context) (string, error) { panic("zero") }

	got, err := r8e.DoRecover(ctx, fn, nil)

	require.ErrorIs(t, err, r8e.ErrPanic)
	assert.Equal(t, "", got, "T zero value returned on panic")
}

func TestPolicyWithRecoverPanicBecomesError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := r8e.NewPolicy[string]("test-recover", r8e.WithRecover())

	_, err := p.Do(ctx, func(_ context.Context) (string, error) {
		panic("policy panic")
	})

	require.ErrorIs(t, err, r8e.ErrPanic)

	var pe *r8e.PanicError

	require.ErrorAs(t, err, &pe)
	assert.Equal(t, "policy panic", pe.Value)
}

func TestPolicyWithRecoverMetricIncremented(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := r8e.NewPolicy[string]("test-recover-metric", r8e.WithRecover())

	_, _ = p.Do(ctx, func(_ context.Context) (string, error) { //nolint:errcheck // testing metric only
		panic("metric test")
	})

	assert.Equal(t, int64(1), p.Metrics().PanicsRecovered)
}

func TestPolicyWithRecoverHookFires(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	var hookFired bool

	p := r8e.NewPolicy[string]("test-recover-hook",
		r8e.WithHooks(&r8e.Hooks{
			OnPanic: func(_ any) { hookFired = true },
		}),
		r8e.WithRecover(),
	)

	_, _ = p.Do(ctx, func(_ context.Context) (string, error) { //nolint:errcheck // testing hook only
		panic("hook fire test")
	})

	assert.True(t, hookFired)
}

func TestPolicyWithRecoverInsideRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	attempts := 0

	p := r8e.NewPolicy[string]("test-recover-retry",
		r8e.WithRecover(),
		r8e.WithRetry(3, r8e.ConstantBackoff(0)),
	)

	// fn panics on first two attempts, succeeds on the third.
	_, err := p.Do(ctx, func(_ context.Context) (string, error) {
		attempts++
		if attempts < 3 {
			panic(fmt.Sprintf("transient panic %d", attempts))
		}

		return "recovered", nil
	})

	require.NoError(t, err)
	assert.Equal(t, 3, attempts, "should have retried after panics")
}

func TestPolicyWithRecoverNoRecoverWithoutOption(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := r8e.NewPolicy[string]("test-no-recover")

	// Without WithRecover, a panic propagates — we catch it in the test to assert.
	assert.Panics(t, func() {
		_, _ = p.Do(ctx, func(_ context.Context) (string, error) { //nolint:errcheck // testing panic
			panic("unrecovered")
		})
	})
}

func TestPanicErrorIsMethod(t *testing.T) {
	t.Parallel()

	pe := &r8e.PanicError{Value: "test"}

	assert.True(t, pe.Is(r8e.ErrPanic), "must match ErrPanic")
	assert.False(t, pe.Is(r8e.ErrTimeout), "must not match unrelated sentinel")
}

func TestPanicErrorErrorMethod(t *testing.T) {
	t.Parallel()

	pe := &r8e.PanicError{Value: "oops"}

	assert.Equal(t, "recovered panic: oops", pe.Error())
}

func TestDoRecoverPanickingHookDoesNotEscape(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// OnPanic itself panics — must NOT escape DoRecover; original error is returned.
	hooks := &r8e.Hooks{
		OnPanic: func(_ any) { panic("hook bug") },
	}
	fn := func(_ context.Context) (string, error) { panic("original") }

	assert.NotPanics(t, func() {
		_, err := r8e.DoRecover(ctx, fn, hooks)

		require.ErrorIs(t, err, r8e.ErrPanic)
	})
}

func TestPolicyWithRecoverMultiplePanics(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	p := r8e.NewPolicy[string]("test-recover-multi", r8e.WithRecover())

	for i := range 3 {
		_, err := p.Do(ctx, func(_ context.Context) (string, error) {
			panic(i)
		})

		require.ErrorIs(t, err, r8e.ErrPanic)
	}

	assert.Equal(t, int64(3), p.Metrics().PanicsRecovered)
}
