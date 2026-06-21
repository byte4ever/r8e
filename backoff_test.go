package r8e_test

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Interface compile checks
// ---------------------------------------------------------------------------

func TestBackoffStrategyInterfaceCompliance(t *testing.T) {
	t.Parallel()

	var _ r8e.BackoffStrategy = r8e.ConstantBackoff(time.Second)
	var _ r8e.BackoffStrategy = r8e.ExponentialBackoff(time.Second)
	var _ r8e.BackoffStrategy = r8e.LinearBackoff(time.Second)
	var _ r8e.BackoffStrategy = r8e.ExponentialJitterBackoff(time.Second)
	var _ r8e.BackoffStrategy = r8e.BackoffFunc(func(attempt int) time.Duration {
		return time.Second
	})
}

// ---------------------------------------------------------------------------
// ConstantBackoff
// ---------------------------------------------------------------------------

func TestConstantBackoff(t *testing.T) {
	t.Parallel()

	b := r8e.ConstantBackoff(250 * time.Millisecond)
	for attempt := range 10 {
		got := b.Delay(attempt)
		require.Equalf(t, 250*time.Millisecond, got, "attempt %d", attempt)
	}
}

// ---------------------------------------------------------------------------
// ExponentialBackoff
// ---------------------------------------------------------------------------

func TestExponentialBackoff(t *testing.T) {
	t.Parallel()

	b := r8e.ExponentialBackoff(100 * time.Millisecond)

	want := []time.Duration{
		100 * time.Millisecond, // 100ms * 2^0
		200 * time.Millisecond, // 100ms * 2^1
		400 * time.Millisecond, // 100ms * 2^2
		800 * time.Millisecond, // 100ms * 2^3
	}

	for i, w := range want {
		got := b.Delay(i)
		require.Equalf(t, w, got, "attempt %d", i)
	}
}

// ---------------------------------------------------------------------------
// LinearBackoff
// ---------------------------------------------------------------------------

func TestLinearBackoff(t *testing.T) {
	t.Parallel()

	b := r8e.LinearBackoff(200 * time.Millisecond)

	want := []time.Duration{
		200 * time.Millisecond, // 200ms * (0+1)
		400 * time.Millisecond, // 200ms * (1+1)
		600 * time.Millisecond, // 200ms * (2+1)
	}

	for i, w := range want {
		got := b.Delay(i)
		require.Equalf(t, w, got, "attempt %d", i)
	}
}

// ---------------------------------------------------------------------------
// ExponentialJitterBackoff
// ---------------------------------------------------------------------------

func TestExponentialJitterBackoff(t *testing.T) {
	t.Parallel()

	base := 100 * time.Millisecond
	b := r8e.ExponentialJitterBackoff(base)

	for attempt := range 5 {
		maxDelay := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
		for range 100 {
			got := b.Delay(attempt)
			require.GreaterOrEqualf(t, got, time.Duration(0), "attempt %d", attempt)
			require.LessOrEqualf(t, got, maxDelay, "attempt %d", attempt)
		}
	}
}

func TestExponentialJitterBackoffDistribution(t *testing.T) {
	t.Parallel()

	// Verify jitter produces some variation (not always zero or always max).
	base := 100 * time.Millisecond
	b := r8e.ExponentialJitterBackoff(base)

	var sawNonZero, sawNonMax bool
	maxDelay := time.Duration(float64(base) * math.Pow(2, float64(3)))
	for range 100 {
		got := b.Delay(3)
		if got > 0 {
			sawNonZero = true
		}
		if got < maxDelay {
			sawNonMax = true
		}
		if sawNonZero && sawNonMax {
			return
		}
	}
	require.True(t, sawNonZero, "jitter always returned 0")
	require.True(t, sawNonMax, "jitter always returned max")
}

func TestExponentialJitterBackoffZeroBase(t *testing.T) {
	t.Parallel()

	// A zero base should always return 0 (exercises the max <= 0 guard).
	b := r8e.ExponentialJitterBackoff(0)
	for attempt := range 5 {
		got := b.Delay(attempt)
		require.Equalf(t, time.Duration(0), got, "attempt %d", attempt)
	}
}

// ---------------------------------------------------------------------------
// BackoffFunc
// ---------------------------------------------------------------------------

func TestBackoffFunc(t *testing.T) {
	t.Parallel()

	custom := r8e.BackoffFunc(func(attempt int) time.Duration {
		return time.Duration(attempt*attempt) * time.Millisecond
	})

	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 0},
		{1, 1 * time.Millisecond},
		{2, 4 * time.Millisecond},
		{3, 9 * time.Millisecond},
		{10, 100 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("attempt-%d", tt.attempt), func(t *testing.T) {
			t.Parallel()

			got := custom.Delay(tt.attempt)
			require.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Benchmarks
// ---------------------------------------------------------------------------

func BenchmarkExponentialBackoff(b *testing.B) {
	s := r8e.ExponentialBackoff(100 * time.Millisecond)
	for b.Loop() {
		s.Delay(5)
	}
}

func BenchmarkExponentialJitterBackoff(b *testing.B) {
	s := r8e.ExponentialJitterBackoff(100 * time.Millisecond)
	for b.Loop() {
		s.Delay(5)
	}
}

// FuzzBackoffDelay asserts every backoff strategy returns a non-negative delay
// and never panics for any attempt — including the large values where the
// 2^attempt computation overflows int64 (which previously yielded a negative
// delay from ExponentialBackoff and a panic from ExponentialJitterBackoff).
func FuzzBackoffDelay(f *testing.F) {
	for _, attempt := range []int{0, 1, 5, 30, 40, 63, 64, 100, 1000, -1, -100, math.MaxInt32} {
		f.Add(attempt)
	}

	base := 100 * time.Millisecond
	strategies := map[string]r8e.BackoffStrategy{
		"constant":    r8e.ConstantBackoff(base),
		"exponential": r8e.ExponentialBackoff(base),
		"linear":      r8e.LinearBackoff(base),
		"jitter":      r8e.ExponentialJitterBackoff(base),
	}

	f.Fuzz(func(t *testing.T, attempt int) {
		for name, strategy := range strategies {
			got := strategy.Delay(attempt) // must not panic
			if got < 0 {
				t.Errorf("%s.Delay(%d) = %v, want >= 0", name, attempt, got)
			}
		}
	})
}
