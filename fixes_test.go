package r8e

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	// timeout, circuit breaker, retry, rate limit, bulkhead, hedge.
	require.Len(t, opts, 6)

	// The options must build a working policy.
	p := NewPolicy[string]("built", append(opts, WithClock(newPolicyClock()))...)
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

func TestBuildOptionsErrorPaths(t *testing.T) {
	t.Parallel()

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
		{
			// max_attempts is required: omitting it must error, not silently
			// collapse the retry to a single attempt.
			"retry without max_attempts",
			&PolicyConfig{Retry: &RetryConfig{Backoff: &backoff, BaseDelay: &good}},
			"max_attempts",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := BuildOptions(tt.pc)
			require.Error(t, err)
			require.ErrorContains(t, err, tt.wantSub)
		})
	}
}

// ---------------------------------------------------------------------------
// Reconfigure — concurrent callers are serialized by the per-policy mutex, so
// the load-modify-store of the shared timeBudget cell cannot lose an update.
// ---------------------------------------------------------------------------

func TestPolicyReconfigureConcurrent(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string](
		"reconf-concurrent",
		WithTimeout(time.Second),
		WithTimeBudget(time.Second),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
		WithClock(newPolicyClock()),
	)

	budgetA, budgetB := "2s", "5s"
	enable, disable := true, false

	const goroutines = 50

	errs := make([]error, goroutines)

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for i := range goroutines {
		go func() {
			defer wg.Done()

			cfg := PolicyConfig{TimeBudget: &budgetA, PropagateDeadline: &enable}
			if i%2 == 0 {
				cfg = PolicyConfig{TimeBudget: &budgetB, PropagateDeadline: &disable}
			}

			errs[i] = p.Reconfigure(cfg)
		}()
	}

	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}

	// The policy remains usable after the concurrent reconfigures.
	result, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) { return "ok", nil },
	)
	require.NoError(t, err)
	require.Equal(t, "ok", result)
}

// ---------------------------------------------------------------------------
// Circuit breaker — half-open admission is bounded by halfOpenMaxAttempts.
// ---------------------------------------------------------------------------

func TestCircuitBreakerHalfOpenBoundsConcurrentProbes(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(1),
	)

	cb.RecordFailure() // open
	clk.setElapsed(2 * time.Second)

	// First Allow transitions to half-open and takes the only probe slot.
	require.NoError(t, cb.Allow())
	// Second concurrent Allow must be rejected — no probe slot left.
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)

	// After the probe succeeds, the breaker closes and admits calls again.
	cb.RecordSuccess()
	require.Equal(t, CircuitClosed, cb.State())
	require.NoError(t, cb.Allow())
}

// TestCircuitBreakerHookMayReenter verifies lifecycle hooks fire OUTSIDE the
// breaker's lock: a hook that re-enters the breaker (here, calls State) must
// not deadlock. With hooks emitted under cb.mu this would hang.
func TestCircuitBreakerHookMayReenter(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}

	var (
		cb          *CircuitBreaker
		stateInHook CircuitState
	)

	hooks := &Hooks{
		OnCircuitOpen: func() { stateInHook = cb.State() },
	}
	cb = NewCircuitBreaker(clk, hooks, FailureThreshold(1))

	done := make(chan struct{})
	go func() {
		cb.RecordFailure() // opens the breaker, firing OnCircuitOpen
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RecordFailure deadlocked — hook ran while holding cb.mu")
	}

	require.Equal(t, CircuitOpen, stateInHook)
}

func TestCircuitBreakerHalfOpenAdmitsUpToMax(t *testing.T) {
	t.Parallel()

	clk := &stubClock{now: time.Now()}
	cb := NewCircuitBreaker(clk, &Hooks{},
		FailureThreshold(1),
		RecoveryTimeout(1*time.Second),
		HalfOpenMaxAttempts(2),
	)

	cb.RecordFailure() // open
	clk.setElapsed(2 * time.Second)

	// First Allow transitions to half-open (probe slot 1).
	require.NoError(t, cb.Allow())
	// Second Allow is admitted as probe slot 2 (max is 2).
	require.NoError(t, cb.Allow())
	// Third Allow exceeds the probe budget.
	require.ErrorIs(t, cb.Allow(), ErrCircuitOpen)
}

// ---------------------------------------------------------------------------
// Bulkhead — Release without a matching Acquire is a no-op, not a negative
// counter that would silently disable the limiter.
// ---------------------------------------------------------------------------

func TestBulkheadReleaseWithoutAcquireIsNoOp(t *testing.T) {
	t.Parallel()

	bh := NewBulkhead(1, RealClock{}, &Hooks{})

	// Unpaired releases must not drive the counter below zero.
	bh.Release()
	bh.Release()

	require.False(t, bh.Full(), "Full() = true after spurious releases, want false")

	// The single slot is still enforced.
	require.NoError(t, bh.Acquire(t.Context()))
	require.ErrorIs(t, bh.Acquire(t.Context()), ErrBulkheadFull)
}

// ---------------------------------------------------------------------------
// Fallback — a value/func typed for a different T than the policy is a
// programmer error and panics at construction rather than being silently
// dropped.
// ---------------------------------------------------------------------------

func TestWithFallbackTypeMismatchPanics(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r, "NewPolicy did not panic on fallback type mismatch")
		if msg, ok := r.(string); ok {
			assert.Contains(t, msg, "WithFallback",
				"panic message = %q, want it to mention WithFallback", msg)
		}
	}()

	// int fallback on a string policy.
	_ = NewPolicy[string]("mismatch", WithFallback(42))
}

func TestWithFallbackFuncTypeMismatchPanics(t *testing.T) {
	require.Panics(t, func() {
		// func returning int on a string policy.
		_ = NewPolicy[string](
			"mismatch-func",
			WithFallbackFunc(func(error) (int, error) { return 0, nil }),
		)
	}, "NewPolicy did not panic on fallback func type mismatch")
}
