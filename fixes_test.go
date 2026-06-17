package r8e

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// BuildOptions — exercised directly in-package (the file-loading tests live in
// the r8econf package).
// ---------------------------------------------------------------------------

func TestBuildOptionsAllFields(t *testing.T) {
	timeout := "2s"
	recovery := "30s"
	threshold := 5
	halfOpen := 2
	backoff := "exponential"
	baseDelay := "100ms"
	maxDelay := "30s"
	maxAttempts := 3
	rate := 100.0
	bulkhead := 10
	hedge := "200ms"

	pc := &PolicyConfig{
		Timeout: &timeout,
		Hedge:   &hedge,
		CircuitBreaker: &CircuitBreakerConfig{
			RecoveryTimeout:     &recovery,
			FailureThreshold:    &threshold,
			HalfOpenMaxAttempts: &halfOpen,
		},
		Retry: &RetryConfig{
			Backoff:     &backoff,
			BaseDelay:   &baseDelay,
			MaxDelay:    &maxDelay,
			MaxAttempts: &maxAttempts,
		},
		RateLimit: &rate,
		Bulkhead:  &bulkhead,
	}

	opts, err := BuildOptions(pc)
	if err != nil {
		t.Fatalf("BuildOptions() error = %v, want nil", err)
	}
	// timeout, circuit breaker, retry, rate limit, bulkhead, hedge.
	if len(opts) != 6 {
		t.Fatalf("len(opts) = %d, want 6", len(opts))
	}

	// The options must build a working policy.
	p := NewPolicy[string]("built", append(opts, WithClock(newPolicyClock()))...)
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "ok", nil },
	)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if result != "ok" {
		t.Fatalf("Do() = %q, want %q", result, "ok")
	}
}

func TestBuildOptionsErrorPaths(t *testing.T) {
	bad := "not-a-duration"
	good := "100ms"
	backoff := "constant"

	tests := []struct {
		name    string
		pc      *PolicyConfig
		wantSub string
	}{
		{
			"bad timeout",
			&PolicyConfig{Timeout: &bad},
			"timeout",
		},
		{
			"bad recovery_timeout",
			&PolicyConfig{CircuitBreaker: &CircuitBreakerConfig{RecoveryTimeout: &bad}},
			"circuit_breaker.recovery_timeout",
		},
		{
			"bad retry base_delay",
			&PolicyConfig{Retry: &RetryConfig{Backoff: &backoff, BaseDelay: &bad}},
			"base_delay",
		},
		{
			"bad retry max_delay",
			&PolicyConfig{Retry: &RetryConfig{Backoff: &backoff, BaseDelay: &good, MaxDelay: &bad}},
			"retry.max_delay",
		},
		{
			"bad hedge",
			&PolicyConfig{Hedge: &bad},
			"hedge",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildOptions(tt.pc)
			if err == nil {
				t.Fatalf("BuildOptions() error = nil, want %q", tt.wantSub)
			}
			if !strings.Contains(err.Error(), tt.wantSub) {
				t.Fatalf("error = %q, want to contain %q", err.Error(), tt.wantSub)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Circuit breaker — half-open admission is bounded by halfOpenMaxAttempts.
// ---------------------------------------------------------------------------

func TestCircuitBreakerHalfOpenBoundsConcurrentProbes(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
	)

	cb.RecordFailure() // open
	clk.setElapsed(2 * time.Second)

	// First Allow transitions to half-open and takes the only probe slot.
	if err := cb.Allow(); err != nil {
		t.Fatalf("first Allow() = %v, want nil", err)
	}
	// Second concurrent Allow must be rejected — no probe slot left.
	if err := cb.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("second Allow() = %v, want ErrCircuitOpen", err)
	}

	// After the probe succeeds, the breaker closes and admits calls again.
	cb.RecordSuccess()
	if got := cb.State(); got != "closed" {
		t.Fatalf("State() = %q, want %q", got, "closed")
	}
	if err := cb.Allow(); err != nil {
		t.Fatalf("Allow() after close = %v, want nil", err)
	}
}

func TestCircuitBreakerHalfOpenAdmitsUpToMax(t *testing.T) {
	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(2),
	)

	cb.RecordFailure() // open
	clk.setElapsed(2 * time.Second)

	// First Allow transitions to half-open (probe slot 1).
	if err := cb.Allow(); err != nil {
		t.Fatalf("first Allow() = %v, want nil", err)
	}
	// Second Allow is admitted as probe slot 2 (max is 2).
	if err := cb.Allow(); err != nil {
		t.Fatalf("second Allow() = %v, want nil", err)
	}
	// Third Allow exceeds the probe budget.
	if err := cb.Allow(); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("third Allow() = %v, want ErrCircuitOpen", err)
	}
}

// ---------------------------------------------------------------------------
// Bulkhead — Release without a matching Acquire is a no-op, not a negative
// counter that would silently disable the limiter.
// ---------------------------------------------------------------------------

func TestBulkheadReleaseWithoutAcquireIsNoOp(t *testing.T) {
	bh := NewBulkhead(1, &Hooks{})

	// Unpaired releases must not drive the counter below zero.
	bh.Release()
	bh.Release()

	if bh.Full() {
		t.Fatal("Full() = true after spurious releases, want false")
	}

	// The single slot is still enforced.
	if err := bh.Acquire(); err != nil {
		t.Fatalf("Acquire() = %v, want nil", err)
	}
	if err := bh.Acquire(); !errors.Is(err, ErrBulkheadFull) {
		t.Fatalf("second Acquire() = %v, want ErrBulkheadFull", err)
	}
}

// ---------------------------------------------------------------------------
// Fallback — a value/func typed for a different T than the policy is a
// programmer error and panics at construction rather than being silently
// dropped.
// ---------------------------------------------------------------------------

func TestWithFallbackTypeMismatchPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("NewPolicy did not panic on fallback type mismatch")
		}
		if msg, ok := r.(string); ok &&
			!strings.Contains(msg, "WithFallback") {
			t.Fatalf("panic message = %q, want it to mention WithFallback", msg)
		}
	}()

	// int fallback on a string policy.
	_ = NewPolicy[string]("mismatch", WithFallback(42))
}

func TestWithFallbackFuncTypeMismatchPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("NewPolicy did not panic on fallback func type mismatch")
		}
	}()

	// func returning int on a string policy.
	_ = NewPolicy[string](
		"mismatch-func",
		WithFallbackFunc(func(error) (int, error) { return 0, nil }),
	)
}
