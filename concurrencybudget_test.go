package r8e

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// concBudgetState reads the budget's counters and tunables coherently under its
// lock. Used to assert invariants survive a concurrent Reconfigure.
func concBudgetState(b *ConcurrencyBudget) (executions, inUse int, maxRatio float64, minConc int) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.executions, b.inUse, b.maxRatio, b.minConcurrency
}

// ---------------------------------------------------------------------------
// Construction and clamping
// ---------------------------------------------------------------------------

func TestConcurrencyBudgetDefaults(t *testing.T) {
	t.Parallel()

	b := NewConcurrencyBudget()

	_, _, maxRatio, minConc := concBudgetState(b)
	assert.InDelta(t, defaultConcurrencyMaxRatio, maxRatio, 1e-9)
	assert.Equal(t, defaultConcurrencyMinConcurrency, minConc)
}

func TestConcurrencyBudgetClampsInvalidParams(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		opts         []ConcurrencyBudgetOption
		wantMaxRatio float64
		wantMinConc  int
	}{
		{
			name:         "non-positive rate falls back to default",
			opts:         []ConcurrencyBudgetOption{MaxRatio(0)},
			wantMaxRatio: defaultConcurrencyMaxRatio,
			wantMinConc:  defaultConcurrencyMinConcurrency,
		},
		{
			name:         "negative rate falls back to default",
			opts:         []ConcurrencyBudgetOption{MaxRatio(-0.5)},
			wantMaxRatio: defaultConcurrencyMaxRatio,
			wantMinConc:  defaultConcurrencyMinConcurrency,
		},
		{
			name:         "rate above one is clamped to one",
			opts:         []ConcurrencyBudgetOption{MaxRatio(2.5)},
			wantMaxRatio: 1,
			wantMinConc:  defaultConcurrencyMinConcurrency,
		},
		{
			name:         "negative floor falls back to default",
			opts:         []ConcurrencyBudgetOption{MinConcurrency(-3)},
			wantMaxRatio: defaultConcurrencyMaxRatio,
			wantMinConc:  defaultConcurrencyMinConcurrency,
		},
		{
			name:         "zero floor is allowed (no floor)",
			opts:         []ConcurrencyBudgetOption{MaxRatio(0.5), MinConcurrency(0)},
			wantMaxRatio: 0.5,
			wantMinConc:  0,
		},
		{
			name:         "valid values pass through unchanged",
			opts:         []ConcurrencyBudgetOption{MaxRatio(0.4), MinConcurrency(7)},
			wantMaxRatio: 0.4,
			wantMinConc:  7,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := NewConcurrencyBudget(tc.opts...)

			_, _, maxRatio, minConc := concBudgetState(b)
			assert.InDelta(t, tc.wantMaxRatio, maxRatio, 1e-9)
			assert.Equal(t, tc.wantMinConc, minConc)
		})
	}
}

// ---------------------------------------------------------------------------
// Admission: ratio scaled by in-flight executions, with a floor
// ---------------------------------------------------------------------------

func TestConcurrencyBudgetAdmissionRatio(t *testing.T) {
	t.Parallel()

	// 20 executions, MaxRatio 0.1, MinConcurrency 2 -> ceiling max(2, 2) = 2.
	b := NewConcurrencyBudget(MaxRatio(0.1), MinConcurrency(2))
	for range 20 {
		b.enter()
	}

	assert.True(t, b.tryAcquire(), "first retry under the ceiling")
	assert.True(t, b.tryAcquire(), "second retry hits the ceiling exactly")
	assert.False(t, b.tryAcquire(), "third retry is over the ceiling")
	assert.Equal(t, 2, b.InUse())
	assert.True(t, b.Exhausted())

	// Releasing one permit frees a slot for a new retry.
	b.release()
	assert.False(t, b.Exhausted())
	assert.True(t, b.tryAcquire())
	assert.Equal(t, 2, b.InUse())
}

func TestConcurrencyBudgetMinConcurrencyFloor(t *testing.T) {
	t.Parallel()

	// One execution, MaxRatio 0.1 -> ratio term 0; the floor of 3 still admits.
	b := NewConcurrencyBudget(MaxRatio(0.1), MinConcurrency(3))
	b.enter()

	assert.True(t, b.tryAcquire())
	assert.True(t, b.tryAcquire())
	assert.True(t, b.tryAcquire())
	assert.False(t, b.tryAcquire(), "ceiling is the floor of 3")
}

func TestConcurrencyBudgetZeroFloorRejectsWhenRatioFloorsToZero(t *testing.T) {
	t.Parallel()

	// MinConcurrency 0 with a single execution and the default rate: the ceiling
	// floors to int(0.25*1) = 0, so every retry is rejected. This is the
	// deterministic lever the retry/hedge gating tests use.
	b := NewConcurrencyBudget(MinConcurrency(0))
	b.enter()

	assert.False(t, b.tryAcquire())
	assert.True(t, b.Exhausted())
}

// ---------------------------------------------------------------------------
// Counter floors and nil-safety
// ---------------------------------------------------------------------------

func TestConcurrencyBudgetExitFlooredAtZero(t *testing.T) {
	t.Parallel()

	b := NewConcurrencyBudget()
	b.exit() // unmatched exit must not drive executions negative

	executions, _, _, _ := concBudgetState(b)
	assert.Equal(t, 0, executions)

	b.enter()
	b.exit()
	b.exit()

	executions, _, _, _ = concBudgetState(b)
	assert.Equal(t, 0, executions)
}

func TestConcurrencyBudgetReleaseFlooredAtZero(t *testing.T) {
	t.Parallel()

	b := NewConcurrencyBudget()
	b.release() // unmatched release must not drive inUse negative

	assert.Equal(t, 0, b.InUse())
}

func TestConcurrencyBudgetNilSafe(t *testing.T) {
	t.Parallel()

	var b *ConcurrencyBudget

	assert.True(t, b.tryAcquire(), "nil budget always grants")
	assert.NotPanics(t, func() {
		b.enter()
		b.exit()
		b.release()
	})
	assert.Equal(t, 0, b.InUse())
	assert.False(t, b.Exhausted())
}

// ---------------------------------------------------------------------------
// Reconfigure
// ---------------------------------------------------------------------------

func TestConcurrencyBudgetReconfigurePreservesUnset(t *testing.T) {
	t.Parallel()

	b := NewConcurrencyBudget(MaxRatio(0.5), MinConcurrency(8))

	// Updating only the floor preserves the rate.
	b.Reconfigure(MinConcurrency(3))

	_, _, maxRatio, minConc := concBudgetState(b)
	assert.InDelta(t, 0.5, maxRatio, 1e-9)
	assert.Equal(t, 3, minConc)

	// Updating only the rate preserves the floor.
	b.Reconfigure(MaxRatio(0.2))

	_, _, maxRatio, minConc = concBudgetState(b)
	assert.InDelta(t, 0.2, maxRatio, 1e-9)
	assert.Equal(t, 3, minConc)
}

func TestConcurrencyBudgetReconfigureClampsAndKeepsCounts(t *testing.T) {
	t.Parallel()

	b := NewConcurrencyBudget(MaxRatio(0.5), MinConcurrency(4))
	b.enter()
	b.enter()
	require.True(t, b.tryAcquire())

	// Invalid overlay is clamped; live counts are untouched.
	b.Reconfigure(MaxRatio(-1), MinConcurrency(-1))

	executions, inUse, maxRatio, minConc := concBudgetState(b)
	assert.Equal(t, 2, executions)
	assert.Equal(t, 1, inUse)
	assert.InDelta(t, defaultConcurrencyMaxRatio, maxRatio, 1e-9)
	assert.Equal(t, defaultConcurrencyMinConcurrency, minConc)
}

// ---------------------------------------------------------------------------
// Retry gating through DoRetry
// ---------------------------------------------------------------------------

func TestDoRetrySuppressedByConcurrencyBudget(t *testing.T) {
	t.Parallel()

	down := errors.New("down")
	budget := NewConcurrencyBudget(MinConcurrency(0)) // ceiling floors to 0

	var (
		shed     int
		attempts int
	)

	hooks := &Hooks{OnConcurrencyBudgetExceeded: func() { shed++ }}

	_, err := DoRetry(
		context.Background(),
		func(_ context.Context) (int, error) {
			attempts++

			return 0, Transient(down)
		},
		RetryParams{
			MaxAttempts: 4,
			Strategy:    ConstantBackoff(0),
			Hooks:       hooks,
			Clock:       RealClock{},
			Concurrency: budget,
		},
	)

	require.ErrorIs(t, err, ErrConcurrencyBudgetExceeded)
	require.ErrorIs(t, err, down, "wraps the last downstream error")
	assert.Equal(t, 1, attempts, "only the ungated first attempt ran")
	assert.Equal(t, 1, shed)
	assert.Equal(t, 0, budget.InUse(), "the denied retry leaks no permit")
}

func TestDoRetryFloorAdmitsLoneRetrier(t *testing.T) {
	t.Parallel()

	down := errors.New("down")
	budget := NewConcurrencyBudget() // default floor of 5 admits a single call

	var attempts int

	_, err := DoRetry(
		context.Background(),
		func(_ context.Context) (int, error) {
			attempts++

			return 0, Transient(down)
		},
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(0),
			Clock:       RealClock{},
			Concurrency: budget,
		},
	)

	require.ErrorIs(t, err, ErrRetriesExhausted)
	require.NotErrorIs(t, err, ErrConcurrencyBudgetExceeded)
	assert.Equal(t, 3, attempts, "all attempts ran under the floor")
	assert.Equal(t, 0, budget.InUse(), "every permit was released")
}

// TestDoRetryReleasesPermitWhenAttemptTimesOut pins the per-attempt-timeout ×
// budget interaction: a retry that acquires a permit then times out under its
// per-attempt deadline must still release the permit (the deferred release in
// runRetryAttempt), so a timing-out retry cannot leak budget.
func TestDoRetryReleasesPermitWhenAttemptTimesOut(t *testing.T) {
	t.Parallel()

	budget := NewConcurrencyBudget() // floor of 5 admits the lone retrier

	var attempts int

	_, err := DoRetry(
		context.Background(),
		func(ctx context.Context) (int, error) {
			attempts++
			if attempts == 1 {
				return 0, Transient(errors.New("first fails fast"))
			}
			// Retry attempts block until the per-attempt timeout fires.
			<-ctx.Done()

			return 0, ctx.Err()
		},
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(0),
			Clock:       RealClock{},
			Concurrency: budget,
			Opts:        []RetryOption{PerAttemptTimeout(5 * time.Millisecond)},
		},
	)

	require.Error(t, err)
	assert.Equal(t, 0, budget.InUse(),
		"a timed-out retry releases its permit")
}

// TestDoRetryReleasesPermitWhenAttemptPanics pins the panic-safety of the permit
// release: a retry whose fn panics (no WithRecover boundary) must still release
// its permit on the unwind, or a panicking retry would leak budget permanently.
func TestDoRetryReleasesPermitWhenAttemptPanics(t *testing.T) {
	t.Parallel()

	budget := NewConcurrencyBudget() // floor of 5 admits the lone retrier

	var attempts int

	require.Panics(t, func() {
		_, _ = DoRetry(
			context.Background(),
			func(_ context.Context) (int, error) {
				attempts++
				if attempts == 1 {
					return 0, Transient(errors.New("first fails fast"))
				}

				panic("boom on retry")
			},
			RetryParams{
				MaxAttempts: 3,
				Strategy:    ConstantBackoff(0),
				Clock:       RealClock{},
				Concurrency: budget,
			},
		)
	})

	assert.Equal(t, 0, budget.InUse(),
		"a panicking retry releases its permit on the unwind")
}

// ---------------------------------------------------------------------------
// Hedge gating through DoHedge
// ---------------------------------------------------------------------------

func TestDoHedgeSkippedByConcurrencyBudget(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		budget := NewConcurrencyBudget(MinConcurrency(0)) // ceiling floors to 0

		var (
			hedgeTriggered bool
			shed           bool
			calls          int
		)

		hooks := &Hooks{
			OnHedgeTriggered:            func() { hedgeTriggered = true },
			OnConcurrencyBudgetExceeded: func() { shed = true },
		}

		result, err := DoHedge(
			context.Background(),
			func(ctx context.Context) (string, error) {
				calls++
				// Primary is slow; without a hedge it still completes.
				select {
				case <-time.After(time.Second):
					return "primary", nil
				case <-ctx.Done():
					return "", ctx.Err()
				}
			},
			HedgeParams{
				Delay:  20 * time.Millisecond,
				Hooks:  hooks,
				Clock:  RealClock{},
				Budget: budget,
			},
		)

		require.NoError(t, err)
		assert.Equal(t, "primary", result)
		assert.False(t, hedgeTriggered, "hedge must not fire when shed")
		assert.True(t, shed, "OnConcurrencyBudgetExceeded fires on the skip")
		assert.Equal(t, 1, calls, "only the primary ran")
		assert.Equal(t, 0, budget.InUse())
	})
}

func TestDoHedgeFiresUnderFloorAndReleasesPermit(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		budget := NewConcurrencyBudget() // floor of 5 admits the hedge

		var hedgeTriggered bool

		hooks := &Hooks{OnHedgeTriggered: func() { hedgeTriggered = true }}

		var calls atomic.Int32

		result, err := DoHedge(
			context.Background(),
			func(ctx context.Context) (string, error) {
				if calls.Add(1) == 1 {
					select {
					case <-time.After(5 * time.Second):
						return "primary-late", nil
					case <-ctx.Done():
						return "", ctx.Err()
					}
				}

				return "hedge", nil
			},
			HedgeParams{
				Delay:  20 * time.Millisecond,
				Hooks:  hooks,
				Clock:  RealClock{},
				Budget: budget,
			},
		)

		require.NoError(t, err)
		assert.Equal(t, "hedge", result)
		assert.True(t, hedgeTriggered)
		// Let the hedge goroutine's deferred release run.
		synctest.Wait()
		assert.Equal(t, 0, budget.InUse(), "the hedge permit was released")
	})
}

// ---------------------------------------------------------------------------
// Policy wiring
// ---------------------------------------------------------------------------

func TestWithConcurrencyBudgetWithoutConsumerPanics(t *testing.T) {
	t.Parallel()

	// Neither retry nor hedge: the budget would gate nothing.
	assert.PanicsWithValue(t, ErrConcurrencyBudgetWithoutConsumer, func() {
		_ = NewPolicy[string]("p", WithConcurrencyBudget())
	})

	// A hedge alone is enough of a consumer.
	assert.NotPanics(t, func() {
		_ = NewPolicy[string]("p",
			WithHedge(10*time.Millisecond), WithConcurrencyBudget())
	})

	// A retry alone is enough of a consumer.
	assert.NotPanics(t, func() {
		_ = NewPolicy[string]("p",
			WithRetry(2, ConstantBackoff(0)), WithConcurrencyBudget())
	})
}

func TestPolicyConcurrencyBudgetSuppressesRetry(t *testing.T) {
	t.Parallel()

	down := errors.New("down")

	var shed int

	policy := NewPolicy[string]("p",
		WithHooks(&Hooks{OnConcurrencyBudgetExceeded: func() { shed++ }}),
		WithRetry(4, ConstantBackoff(0)),
		// Ceiling floors to int(0.1*1) = 0 for a single in-flight call.
		WithConcurrencyBudget(MaxRatio(0.1), MinConcurrency(0)),
	)

	_, err := policy.Do(context.Background(),
		func(_ context.Context) (string, error) {
			return "", Transient(down)
		})

	require.ErrorIs(t, err, ErrConcurrencyBudgetExceeded)
	require.ErrorIs(t, err, down)
	assert.Equal(t, 1, shed)

	metrics := policy.Metrics()
	assert.Equal(t, int64(1), metrics.ConcurrencyBudgetExceeded)
	assert.Equal(t, int64(0), metrics.ConcurrencyBudgetInUse)

	// The budget never gates first attempts, so the policy stays healthy.
	assert.True(t, policy.HealthStatus().Healthy)
}

func TestWithSharedConcurrencyBudget(t *testing.T) {
	t.Parallel()

	// A nil shared budget is ignored: the policy builds and behaves as if no
	// budget were configured.
	assert.NotPanics(t, func() {
		_ = NewPolicy[string]("nil-budget",
			WithRetry(2, ConstantBackoff(0)),
			WithSharedConcurrencyBudget(nil),
		)
	})

	// A real shared budget gates retries on both policies.
	budget := NewConcurrencyBudget(MaxRatio(0.1), MinConcurrency(0))
	down := errors.New("down")

	build := func(name string) *Policy[string] {
		return NewPolicy[string](name,
			WithRetry(3, ConstantBackoff(0)),
			WithSharedConcurrencyBudget(budget),
		)
	}

	for _, p := range []*Policy[string]{build("a"), build("b")} {
		_, err := p.Do(context.Background(),
			func(_ context.Context) (string, error) {
				return "", Transient(down)
			})
		require.ErrorIs(t, err, ErrConcurrencyBudgetExceeded)
	}
}

// ---------------------------------------------------------------------------
// Config build and reconfigure
// ---------------------------------------------------------------------------

func TestBuildOptionsConcurrencyBudget(t *testing.T) {
	t.Parallel()

	rate := 0.1
	minConc := 0
	maxAttempts := 4
	backoff := "constant"
	base := "0s"

	opts, err := BuildOptions(&PolicyConfig{
		Retry: &RetryConfig{
			Backoff: &backoff, BaseDelay: &base, MaxAttempts: &maxAttempts,
		},
		ConcurrencyBudget: &ConcurrencyBudgetConfig{
			MaxRatio: &rate, MinConcurrency: &minConc,
		},
	})
	require.NoError(t, err)

	// The configured 0.1/0 ceiling floors to 0, so a single call's retry is shed
	// — proving the config values reached the budget.
	policy := NewPolicy[string]("cfg", opts...)

	_, doErr := policy.Do(context.Background(),
		func(_ context.Context) (string, error) {
			return "", Transient(errors.New("down"))
		})
	require.ErrorIs(t, doErr, ErrConcurrencyBudgetExceeded)
}

func TestBuildOptionsConcurrencyBudgetWithoutConsumer(t *testing.T) {
	t.Parallel()

	rate := 0.25

	_, err := BuildOptions(&PolicyConfig{
		ConcurrencyBudget: &ConcurrencyBudgetConfig{MaxRatio: &rate},
	})
	require.ErrorIs(t, err, ErrConcurrencyBudgetWithoutConsumer)
	require.ErrorContains(t, err, "concurrency_budget")
}

func TestReconfigureConcurrencyBudget(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string]("p",
		WithRetry(3, ConstantBackoff(0)),
		WithConcurrencyBudget(MaxRatio(0.5), MinConcurrency(5)),
	)

	rate := 0.1
	minConc := 0

	err := policy.Reconfigure(PolicyConfig{
		ConcurrencyBudget: &ConcurrencyBudgetConfig{
			MaxRatio: &rate, MinConcurrency: &minConc,
		},
	})
	require.NoError(t, err)

	// After tightening to a 0-flooring ceiling, retries are shed.
	_, doErr := policy.Do(context.Background(),
		func(_ context.Context) (string, error) {
			return "", Transient(errors.New("down"))
		})
	require.ErrorIs(t, doErr, ErrConcurrencyBudgetExceeded)
}

func TestReconfigureConcurrencyBudgetAbsent(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string]("p", WithRetry(2, ConstantBackoff(0)))

	rate := 0.5

	err := policy.Reconfigure(PolicyConfig{
		ConcurrencyBudget: &ConcurrencyBudgetConfig{MaxRatio: &rate},
	})
	require.ErrorIs(t, err, ErrPatternAbsent,
		"absent budget carries the ErrPatternAbsent sentinel")
	require.ErrorContains(t, err, "concurrency_budget")
}

// ---------------------------------------------------------------------------
// Concurrency: invariants hold under -race with a concurrent Reconfigure
// ---------------------------------------------------------------------------

func TestConcurrencyBudgetConcurrentUse(t *testing.T) {
	t.Parallel()

	b := NewConcurrencyBudget(MaxRatio(0.5), MinConcurrency(4))

	var wg sync.WaitGroup

	for range 50 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			b.enter()
			defer b.exit()

			if b.tryAcquire() {
				_ = b.InUse()
				b.release()
			}

			_ = b.Exhausted()
		}()
	}

	for range 5 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			b.Reconfigure(MaxRatio(0.3), MinConcurrency(2))
		}()
	}

	wg.Wait()

	executions, inUse, _, _ := concBudgetState(b)
	assert.Equal(t, 0, executions, "every enter was matched by an exit")
	assert.Equal(t, 0, inUse, "every acquire was matched by a release")
}
