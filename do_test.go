package r8e

import (
	"context"
	"errors"
	"testing"
	"time"
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
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "hello" {
		t.Fatalf("Do() = %q, want %q", result, "hello")
	}
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
	if err != nil {
		t.Fatalf("Do() error = %v, want nil", err)
	}
	if result != "recovered" {
		t.Fatalf("Do() = %q, want %q", result, "recovered")
	}
	if attempt != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempt)
	}
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

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("Do() error = %v, want ErrTimeout", err)
	}
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
	if err != nil {
		t.Fatalf("Do() error = %v, want nil (fallback served)", err)
	}
	if result != "default-value" {
		t.Fatalf("Do() = %q, want %q", result, "default-value")
	}
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

	if !errors.Is(err, sentinel) {
		t.Fatalf("Do() error = %v, want %v", err, sentinel)
	}
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
