package r8e_test

import (
	"math"
	"testing"
	"time"

	"github.com/byte4ever/r8e"
)

// ---------------------------------------------------------------------------
// Interface compile checks
// ---------------------------------------------------------------------------

func TestBackoffStrategyInterfaceCompliance(t *testing.T) {
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
	b := r8e.ConstantBackoff(250 * time.Millisecond)
	for attempt := range 10 {
		got := b.Delay(attempt)
		if got != 250*time.Millisecond {
			t.Fatalf(
				"attempt %d: Delay() = %v, want %v",
				attempt,
				got,
				250*time.Millisecond,
			)
		}
	}
}

// ---------------------------------------------------------------------------
// ExponentialBackoff
// ---------------------------------------------------------------------------

func TestExponentialBackoff(t *testing.T) {
	b := r8e.ExponentialBackoff(100 * time.Millisecond)

	want := []time.Duration{
		100 * time.Millisecond, // 100ms * 2^0
		200 * time.Millisecond, // 100ms * 2^1
		400 * time.Millisecond, // 100ms * 2^2
		800 * time.Millisecond, // 100ms * 2^3
	}

	for i, w := range want {
		got := b.Delay(i)
		if got != w {
			t.Fatalf("attempt %d: Delay() = %v, want %v", i, got, w)
		}
	}
}

// ---------------------------------------------------------------------------
// LinearBackoff
// ---------------------------------------------------------------------------

func TestLinearBackoff(t *testing.T) {
	b := r8e.LinearBackoff(200 * time.Millisecond)

	want := []time.Duration{
		200 * time.Millisecond, // 200ms * (0+1)
		400 * time.Millisecond, // 200ms * (1+1)
		600 * time.Millisecond, // 200ms * (2+1)
	}

	for i, w := range want {
		got := b.Delay(i)
		if got != w {
			t.Fatalf("attempt %d: Delay() = %v, want %v", i, got, w)
		}
	}
}

// ---------------------------------------------------------------------------
// ExponentialJitterBackoff
// ---------------------------------------------------------------------------

func TestExponentialJitterBackoff(t *testing.T) {
	base := 100 * time.Millisecond
	b := r8e.ExponentialJitterBackoff(base)

	for attempt := range 5 {
		maxDelay := time.Duration(float64(base) * math.Pow(2, float64(attempt)))
		for range 100 {
			got := b.Delay(attempt)
			if got < 0 || got > maxDelay {
				t.Fatalf(
					"attempt %d: Delay() = %v, want in [0, %v]",
					attempt,
					got,
					maxDelay,
				)
			}
		}
	}
}

func TestExponentialJitterBackoffDistribution(t *testing.T) {
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
	if !sawNonZero {
		t.Fatal("jitter always returned 0")
	}
	if !sawNonMax {
		t.Fatal("jitter always returned max")
	}
}

func TestExponentialJitterBackoffZeroBase(t *testing.T) {
	// A zero base should always return 0 (exercises the max <= 0 guard).
	b := r8e.ExponentialJitterBackoff(0)
	for attempt := range 5 {
		got := b.Delay(attempt)
		if got != 0 {
			t.Fatalf("attempt %d: Delay() = %v, want 0", attempt, got)
		}
	}
}

// ---------------------------------------------------------------------------
// BackoffFunc
// ---------------------------------------------------------------------------

func TestBackoffFunc(t *testing.T) {
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
		got := custom.Delay(tt.attempt)
		if got != tt.want {
			t.Fatalf(
				"attempt %d: Delay() = %v, want %v",
				tt.attempt,
				got,
				tt.want,
			)
		}
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
