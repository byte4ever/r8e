package r8e

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// budgetWithinBounds reads tokens and capacity coherently under the budget's
// lock and reports whether the invariant 0 <= tokens <= maxTokens holds. Used to
// assert the invariant survives a concurrent Reconfigure.
func budgetWithinBounds(b *RetryBudget) bool {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.tokens >= 0 && b.tokens <= b.maxTokens
}

// ---------------------------------------------------------------------------
// Unit tests: RetryBudget token accounting
// ---------------------------------------------------------------------------

func TestRetryBudgetStartsFull(t *testing.T) {
	t.Parallel()

	b := NewRetryBudget(MaxTokens(10), TokenRatio(0.1))

	assert.InDelta(t, 10.0, b.Tokens(), 1e-9)
	assert.True(t, b.allowRetry())
	assert.False(t, b.Exhausted())
}

func TestRetryBudgetDefaults(t *testing.T) {
	t.Parallel()

	b := NewRetryBudget()

	assert.InDelta(t, float64(defaultBudgetMaxTokens), b.Tokens(), 1e-9)
}

func TestRetryBudgetClampsInvalidParams(t *testing.T) {
	t.Parallel()

	// MaxTokens below 1 and a non-positive ratio both fall back to defaults.
	b := NewRetryBudget(MaxTokens(0), TokenRatio(-1))

	assert.InDelta(t, float64(defaultBudgetMaxTokens), b.Tokens(), 1e-9)

	// Draining by one and crediting one success must move by exactly the
	// default ratio, proving the ratio was clamped to the default.
	b.recordFailure()
	b.recordSuccess()

	want := float64(defaultBudgetMaxTokens) - 1 + defaultBudgetRatio
	assert.InDelta(t, want, b.Tokens(), 1e-9)
}

func TestRetryBudgetRecordSuccessCapsAtMax(t *testing.T) {
	t.Parallel()

	b := NewRetryBudget(MaxTokens(4), TokenRatio(0.5))

	// Already full: a success cannot push the bucket past its capacity.
	b.recordSuccess()
	assert.InDelta(t, 4.0, b.Tokens(), 1e-9)

	// Drain one, then a single success adds exactly the ratio back.
	b.recordFailure()
	b.recordSuccess()
	assert.InDelta(t, 3.5, b.Tokens(), 1e-9)
}

func TestRetryBudgetRecordFailureFloorsAtZero(t *testing.T) {
	t.Parallel()

	b := NewRetryBudget(MaxTokens(2), TokenRatio(0.1))

	// Three failures against a capacity of two must floor at zero, not go
	// negative.
	b.recordFailure()
	b.recordFailure()
	b.recordFailure()

	assert.InDelta(t, 0.0, b.Tokens(), 1e-9)
}

func TestRetryBudgetAllowRetryThreshold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		maxTokens int
		failures  int
		want      bool
	}{
		{"even full allows", 4, 0, true},
		{"even above half allows", 4, 1, true},
		{"even at half suppresses", 4, 2, false},
		{"even below half suppresses", 4, 3, false},
		// Odd capacity → threshold is 2.5; the boundary falls between tokens.
		{"odd above half allows", 5, 2, true},        // 3 tokens > 2.5
		{"odd crosses half suppresses", 5, 3, false}, // 2 tokens <= 2.5
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := NewRetryBudget(MaxTokens(tc.maxTokens), TokenRatio(0.1))
			for range tc.failures {
				b.recordFailure()
			}

			assert.Equal(t, tc.want, b.allowRetry())
			assert.Equal(t, !tc.want, b.Exhausted())
		})
	}
}

func TestRetryBudgetReconfigure(t *testing.T) {
	t.Parallel()

	b := NewRetryBudget(MaxTokens(10), TokenRatio(0.1))

	// Drain to 60% full (6 of 10), then halve the capacity. The fill fraction
	// is preserved, so tokens become 60% of 5 = 3 — not a raw clamp to 5.
	for range 4 {
		b.recordFailure()
	}

	b.Reconfigure(MaxTokens(5))
	assert.InDelta(t, 3.0, b.Tokens(), 1e-9)

	// Growing the capacity also preserves the fraction: 60% of 20 = 12. A raw
	// clamp would have left tokens at 3.
	b.Reconfigure(MaxTokens(20))
	assert.InDelta(t, 12.0, b.Tokens(), 1e-9)

	// The unspecified ratio is preserved across a partial reconfigure.
	b.recordFailure()
	b.recordSuccess()
	assert.InDelta(t, 12.0-1+0.1, b.Tokens(), 1e-9)

	// Updating only the ratio leaves the capacity (and token level) untouched.
	b.Reconfigure(TokenRatio(0.5))
	assert.InDelta(t, 11.1, b.Tokens(), 1e-9)
	b.recordFailure()
	b.recordSuccess()
	assert.InDelta(t, 11.1-1+0.5, b.Tokens(), 1e-9)
}

func TestRetryBudgetReconfigureZeroValue(t *testing.T) {
	t.Parallel()

	// A zero-value budget (one not built via NewRetryBudget) has no fill
	// fraction to preserve; Reconfigure must initialise it to a full bucket,
	// not leave it drained.
	var b RetryBudget

	b.Reconfigure(MaxTokens(8))

	assert.InDelta(t, 8.0, b.Tokens(), 1e-9)
	assert.False(t, b.Exhausted())
}

func TestRetryBudgetNilIsSafe(t *testing.T) {
	t.Parallel()

	var b *RetryBudget

	// A nil budget never throttles and treats record calls as no-ops, so the
	// retry path can hold one unconditionally.
	assert.True(t, b.allowRetry())
	assert.False(t, b.Exhausted())
	assert.Zero(t, b.Tokens())
	assert.NotPanics(t, func() {
		b.recordSuccess()
		b.recordFailure()
	})
}

func TestRetryBudgetConcurrentAccess(t *testing.T) {
	t.Parallel()

	const (
		maxTokens  = 50
		goroutines = 100
		opsEach    = 200
	)

	b := NewRetryBudget(MaxTokens(maxTokens), TokenRatio(0.3))

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for g := range goroutines {
		go func(g int) {
			defer wg.Done()

			for i := range opsEach {
				switch (g + i) % 3 {
				case 0:
					b.recordSuccess()
				case 1:
					b.recordFailure()
				default:
					_ = b.allowRetry()
				}
			}
		}(g)
	}

	wg.Wait()

	// Whatever the interleaving, the bucket must stay within [0, maxTokens].
	tokens := b.Tokens()
	assert.GreaterOrEqual(t, tokens, 0.0)
	assert.LessOrEqual(t, tokens, float64(maxTokens))
}

func TestRetryBudgetConcurrentExactAccounting(t *testing.T) {
	t.Parallel()

	const (
		workers   = 20
		failsEach = 50
	)

	// Capacity is large enough that no failure floors at zero, so the exact
	// end state is deterministic: every one of workers*failsEach decrements must
	// land. A lost update (a non-atomic read-modify-write) would leave more
	// tokens than expected, which the exact assertion below catches — bounds
	// checks alone would not.
	b := NewRetryBudget(MaxTokens(workers*failsEach*2), TokenRatio(0.1))

	var wg sync.WaitGroup

	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()

			for range failsEach {
				b.recordFailure()
			}
		}()
	}

	wg.Wait()

	start := float64(workers * failsEach * 2)
	assert.InDelta(t, start-workers*failsEach, b.Tokens(), 1e-9)
}

func TestRetryBudgetReconfigureUnderLoad(t *testing.T) {
	t.Parallel()

	const (
		workers = 50
		ops     = 400
	)

	b := NewRetryBudget(MaxTokens(100), TokenRatio(0.5))

	// Workers hammer the bucket while one goroutine repeatedly shrinks and
	// grows the capacity. Shrinking is the hazard: a torn read-then-write could
	// leave tokens above the freshly-lowered capacity.
	var work sync.WaitGroup

	work.Add(workers + 1)

	go func() {
		defer work.Done()

		for i := range ops {
			if i%2 == 0 {
				b.Reconfigure(MaxTokens(2))
			} else {
				b.Reconfigure(MaxTokens(100))
			}
		}
	}()

	for g := range workers {
		go func(g int) {
			defer work.Done()

			for i := range ops {
				switch (g + i) % 3 {
				case 0:
					b.recordSuccess()
				case 1:
					b.recordFailure()
				default:
					_ = b.allowRetry()
				}
			}
		}(g)
	}

	// A checker spins for the whole run asserting the invariant never tears.
	done := make(chan struct{})
	go func() {
		work.Wait()
		close(done)
	}()

	var violated atomic.Bool

	checked := make(chan struct{})

	go func() {
		defer close(checked)

		for {
			if !budgetWithinBounds(b) {
				violated.Store(true)
			}

			select {
			case <-done:
				return
			default:
			}
		}
	}()

	<-checked

	assert.False(t, violated.Load(),
		"0 <= tokens <= maxTokens must hold throughout concurrent reconfigure")
	assert.True(t, budgetWithinBounds(b))
}

// ---------------------------------------------------------------------------
// More DoRetry behavior edge cases
// ---------------------------------------------------------------------------

func TestDoRetryBudgetMaxAttemptsOne(t *testing.T) {
	t.Parallel()

	clk := newImmediateTestClock()

	var exceeded int

	hooks := &Hooks{OnRetryBudgetExceeded: func() { exceeded++ }}
	budget := NewRetryBudget(MaxTokens(10), TokenRatio(0.1))

	attempts := 0

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempts++

			return "", Transient(errors.New("down"))
		},
		RetryParams{
			MaxAttempts: 1,
			Strategy:    ConstantBackoff(time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
			Budget:      budget,
		},
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRetriesExhausted)
	assert.Equal(t, 1, attempts)
	// The single failure is charged, but no retry is gated, so the budget
	// hook never fires.
	assert.Zero(t, exceeded)
	assert.InDelta(t, 9.0, budget.Tokens(), 1e-9)
}

// ---------------------------------------------------------------------------
// DoRetry behavior with a budget
// ---------------------------------------------------------------------------

func TestDoRetryBudgetSuppressesRetries(t *testing.T) {
	t.Parallel()

	clk := newImmediateTestClock()
	downstream := errors.New("boom")

	var exceeded int

	hooks := &Hooks{OnRetryBudgetExceeded: func() { exceeded++ }}
	budget := NewRetryBudget(MaxTokens(4), TokenRatio(0.1))

	attempts := 0

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempts++

			return "", Transient(downstream)
		},
		RetryParams{
			MaxAttempts: 10,
			Strategy:    ConstantBackoff(time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
			Budget:      budget,
		},
	)

	require.Error(t, err)
	// Budget capacity 4 → threshold 2 → the second failure suppresses further
	// retries, so only two attempts run despite MaxAttempts being 10.
	assert.Equal(t, 2, attempts)
	assert.Equal(t, 1, exceeded)
	// The caller receives the real downstream error, not a synthetic one.
	assert.ErrorIs(t, err, downstream)
	assert.NotErrorIs(t, err, ErrRetriesExhausted)
}

func TestDoRetryBudgetAllowsWhenHealthy(t *testing.T) {
	t.Parallel()

	clk := newImmediateTestClock()

	var exceeded int

	hooks := &Hooks{OnRetryBudgetExceeded: func() { exceeded++ }}
	budget := NewRetryBudget(MaxTokens(10), TokenRatio(0.1))

	attempts := 0

	result, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			attempts++
			if attempts < 3 {
				return "", Transient(errors.New("transient"))
			}

			return "ok", nil
		},
		RetryParams{
			MaxAttempts: 5,
			Strategy:    ConstantBackoff(time.Millisecond),
			Hooks:       hooks,
			Clock:       clk,
			Budget:      budget,
		},
	)

	require.NoError(t, err)
	assert.Equal(t, "ok", result)
	assert.Equal(t, 3, attempts)
	assert.Zero(t, exceeded)
	// The success credits the bucket, so it stays above its starting drain.
	assert.False(t, budget.Exhausted())
}

func TestDoRetryNonRetryableDoesNotChargeBudget(t *testing.T) {
	t.Parallel()

	clk := newImmediateTestClock()
	budget := NewRetryBudget(MaxTokens(4), TokenRatio(0.1))

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", Permanent(errors.New("bad request"))
		},
		RetryParams{
			MaxAttempts: 5,
			Strategy:    ConstantBackoff(time.Millisecond),
			Hooks:       &Hooks{},
			Clock:       clk,
			Budget:      budget,
		},
	)

	require.Error(t, err)
	// A permanent failure is not a retry-storm contributor; the bucket is
	// untouched.
	assert.InDelta(t, 4.0, budget.Tokens(), 1e-9)
}

// ---------------------------------------------------------------------------
// Policy integration: panic, health, metrics, reconfigure, sharing
// ---------------------------------------------------------------------------

func TestWithRetryBudgetPanicsWithoutRetry(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(
		t,
		ErrRetryBudgetWithoutRetry,
		func() {
			_ = NewPolicy[string]("", WithRetryBudget(MaxTokens(10)))
		},
	)
}

func TestWithSharedRetryBudgetNilIsIgnored(t *testing.T) {
	t.Parallel()

	// A nil shared budget must not be treated as a configured budget, so the
	// policy builds without the retry-budget pattern.
	p := NewPolicy[string](
		"",
		WithRetry(2, ConstantBackoff(time.Millisecond)),
		WithSharedRetryBudget(nil),
	)

	assert.Nil(t, p.retryBudget)
}

func TestPolicyRetryBudgetHealthAndMetrics(t *testing.T) {
	t.Parallel()

	var userExceeded int

	p := NewPolicy[string](
		"svc",
		WithClock(newImmediateTestClock()),
		WithRegistry(NewRegistry()),
		WithReadinessImpact(),
		WithHooks(&Hooks{
			OnRetryBudgetExceeded: func() { userExceeded++ },
		}),
		WithRetry(10, ConstantBackoff(time.Millisecond)),
		WithRetryBudget(MaxTokens(2), TokenRatio(0.1)),
	)

	_, err := p.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", Transient(errors.New("down"))
		},
	)
	require.Error(t, err)

	// The user hook fires alongside the metrics counter (instrumented hooks).
	assert.Equal(t, 1, userExceeded)

	status := p.HealthStatus()
	assert.Contains(t, status.Conditions, ConditionRetryBudgetExhausted)
	assert.Equal(t, CriticalityDegraded, status.Criticality)
	// Degraded — not critical — so readiness is unaffected even with impact on.
	assert.True(t, status.Healthy)

	metrics := p.Metrics()
	assert.Equal(t, int64(1), metrics.RetryBudgetExceeded)
	assert.InDelta(t, 1.0, metrics.RetryBudgetTokens, 1e-9)
}

func TestPolicyReconfigureRetryBudget(t *testing.T) {
	t.Parallel()

	reg := NewRegistry()
	p := NewPolicy[string](
		"svc",
		WithRegistry(reg),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
		WithRetryBudget(MaxTokens(10), TokenRatio(0.1)),
	)

	maxTokens := 4
	err := reg.Reconfigure("svc", PolicyConfig{
		RetryBudget: &RetryBudgetConfig{MaxTokens: &maxTokens},
	})
	require.NoError(t, err)
	assert.InDelta(t, 4.0, p.retryBudget.Tokens(), 1e-9)
}

func TestReconfigureRetryBudgetAbsent(t *testing.T) {
	t.Parallel()

	p := NewPolicy[string](
		"",
		WithRetry(3, ConstantBackoff(time.Millisecond)),
	)

	maxTokens := 4
	err := p.Reconfigure(PolicyConfig{
		RetryBudget: &RetryBudgetConfig{MaxTokens: &maxTokens},
	})

	require.ErrorIs(t, err, ErrPatternAbsent)
	// The error names the absent pattern for triage.
	assert.ErrorContains(t, err, "retry_budget")
}

func TestBuildOptionsRetryBudget(t *testing.T) {
	t.Parallel()

	maxTokens := 6
	ratio := 0.25
	opts, err := BuildOptions(&PolicyConfig{
		Retry: &RetryConfig{
			Backoff:     strPtr("constant"),
			BaseDelay:   strPtr("1ms"),
			MaxAttempts: intPtr(3),
		},
		RetryBudget: &RetryBudgetConfig{
			MaxTokens:  &maxTokens,
			TokenRatio: &ratio,
		},
	})
	require.NoError(t, err)

	p := NewPolicy[string]("", opts...)
	require.NotNil(t, p.retryBudget)
	assert.InDelta(t, 6.0, p.retryBudget.Tokens(), 1e-9)
}

func TestBuildOptionsRetryBudgetWithoutRetry(t *testing.T) {
	t.Parallel()

	maxTokens := 6
	// A config-sourced budget without a retry block surfaces as an error here,
	// not a panic at NewPolicy.
	_, err := BuildOptions(&PolicyConfig{
		RetryBudget: &RetryBudgetConfig{MaxTokens: &maxTokens},
	})

	require.ErrorIs(t, err, ErrRetryBudgetWithoutRetry)
	// The wrap context names the offending config field for triage.
	assert.ErrorContains(t, err, "retry_budget")
}

func TestWithSharedRetryBudgetCoordinates(t *testing.T) {
	t.Parallel()

	budget := NewRetryBudget(MaxTokens(4), TokenRatio(0.1))

	drainer := NewPolicy[string](
		"",
		WithClock(newImmediateTestClock()),
		WithRetry(10, ConstantBackoff(time.Millisecond)),
		WithSharedRetryBudget(budget),
	)

	_, err := drainer.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", Transient(errors.New("down"))
		},
	)
	require.Error(t, err)
	require.True(t, budget.Exhausted(), "drainer should exhaust the shared budget")

	// A second policy sharing the same budget sees it already exhausted, so its
	// very first failure is not retried.
	follower := NewPolicy[string](
		"",
		WithClock(newImmediateTestClock()),
		WithRetry(10, ConstantBackoff(time.Millisecond)),
		WithSharedRetryBudget(budget),
	)

	var attempts atomic.Int64

	_, err = follower.Do(
		context.Background(),
		func(_ context.Context) (string, error) {
			attempts.Add(1)

			return "", Transient(errors.New("down"))
		},
	)
	require.Error(t, err)
	assert.Equal(t, int64(1), attempts.Load())
}
