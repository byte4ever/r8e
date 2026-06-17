package r8e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TestDoBasic -- Do with no options passes through to fn
// ---------------------------------------------------------------------------

func TestDoBasic(t *testing.T) {
	result, err := Do[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "hello", nil
		},
	)
	require.NoError(t, err)
	require.Equal(t, "hello", result)
}

// ---------------------------------------------------------------------------
// TestDoWithRetry -- Do with WithRetry retries on transient errors
// ---------------------------------------------------------------------------

func TestDoWithRetry(t *testing.T) {
	clk := newPolicyClock()
	attempt := 0

	result, err := Do[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempt++
			if attempt < 3 {
				return "", errors.New("transient")
			}
			return "recovered", nil
		},
		WithClock(clk),
		WithRetry(3, ConstantBackoff(10*time.Millisecond)),
	)
	require.NoError(t, err)
	require.Equal(t, "recovered", result)
	require.Equal(t, 3, attempt)
}

// ---------------------------------------------------------------------------
// TestDoWithTimeout -- Do with WithTimeout returns ErrTimeout on slow fn
// ---------------------------------------------------------------------------

func TestDoWithTimeout(t *testing.T) {
	_, err := Do[string](
		context.Background(),
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		WithTimeout(50*time.Millisecond),
	)

	require.ErrorIs(t, err, ErrTimeout)
}

// ---------------------------------------------------------------------------
// TestDoWithFallback -- Do with WithFallback returns fallback on error
// ---------------------------------------------------------------------------

func TestDoWithFallback(t *testing.T) {
	result, err := Do[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", errors.New("service down")
		},
		WithFallback("default-value"),
	)
	require.NoError(t, err)
	require.Equal(t, "default-value", result)
}

// ---------------------------------------------------------------------------
// TestDoErrorPropagation -- Do propagates fn errors when no patterns configured
// ---------------------------------------------------------------------------

func TestDoErrorPropagation(t *testing.T) {
	sentinel := errors.New("something went wrong")

	_, err := Do[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", sentinel
		},
	)

	require.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// BenchmarkDo -- benchmark the convenience function
// ---------------------------------------------------------------------------

func BenchmarkDo(b *testing.B) {
	ctx := context.Background()

	for b.Loop() {
		_, _ = Do[string](ctx, func(_ context.Context) (string, error) {
			return "ok", nil
		})
	}
}
