package r8e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// budgetClock has a frozen, settable Now and fires timers immediately, so
// time-budget tests are deterministic: remaining = deadline - Now is exact and
// independent of the wall clock, while retry backoff sleeps complete at once.
type budgetClock struct {
	now time.Time
	mu  sync.Mutex
}

func newBudgetClock() *budgetClock {
	return &budgetClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *budgetClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *budgetClock) Since(t time.Time) time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now.Sub(t)
}

//nolint:ireturn // satisfies the r8e.Timer interface
func (c *budgetClock) NewTimer(time.Duration) Timer {
	t := newTestTimer()
	t.fire()

	return t
}

func (c *budgetClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// ---------------------------------------------------------------------------
// timeBudgetRemaining helper
// ---------------------------------------------------------------------------

func TestTimeBudgetRemaining(t *testing.T) {
	t.Parallel()

	clk := newBudgetClock()

	// No budget on the context.
	_, ok := timeBudgetRemaining(context.Background(), clk)
	assert.False(t, ok)

	// A budget five seconds out leaves exactly five seconds (frozen clock).
	ctx := attachTimeBudget(context.Background(), clk.Now().Add(5*time.Second))
	remaining, ok := timeBudgetRemaining(ctx, clk)
	require.True(t, ok)
	assert.Equal(t, 5*time.Second, remaining)

	// After advancing past the deadline, remaining goes negative.
	clk.advance(6 * time.Second)
	remaining, ok = timeBudgetRemaining(ctx, clk)
	require.True(t, ok)
	assert.Negative(t, remaining)
}

// ---------------------------------------------------------------------------
// Retry honors the time budget (frozen clock -> exact, deterministic)
// ---------------------------------------------------------------------------

// budgetedRetryPolicy builds a retrying policy on a frozen clock with the given
// total budget and constant backoff, and returns it plus a *count of attempts*
// updater via the returned Do helper.
func budgetedRetryPolicy(name string, budget, backoff time.Duration) (*Policy[string], *int) {
	exceeded := new(int)

	policy := NewPolicy[string](name,
		WithClock(newBudgetClock()),
		WithHooks(&Hooks{OnTimeBudgetExceeded: func() { *exceeded++ }}),
		WithRetry(5, ConstantBackoff(backoff)),
		WithTimeBudget(budget),
	)

	return policy, exceeded
}

func TestWithTimeBudgetStopsRetryEarly(t *testing.T) {
	t.Parallel()

	// remaining is a *positive* 1ms (not already spent), but the 10ms backoff
	// would overrun it — so the stop is for the advertised "would-exhaust"
	// reason, not the trivial already-spent case.
	policy, exceeded := budgetedRetryPolicy("budgeted", time.Millisecond, 10*time.Millisecond)

	down := errors.New("downstream down")

	attempts := 0
	_, err := policy.Do(context.Background(), func(_ context.Context) (string, error) {
		attempts++

		return "", Transient(down)
	})

	require.ErrorIs(t, err, ErrTimeBudgetExceeded)
	require.ErrorIs(t, err, down) // the real downstream error is wrapped
	assert.Equal(t, 1, attempts, "no retry should run once the budget is spent")
	assert.Equal(t, 1, *exceeded)
	assert.Equal(t, int64(1), policy.Metrics().TimeBudgetExceeded)
}

func TestWithTimeBudgetStopsAtExactBoundary(t *testing.T) {
	t.Parallel()

	// delay == remaining exactly: the condition is `delay >= remaining`, so it
	// stops. This pins the boundary against a `>=`->`>` regression that a
	// real-clock test could never hit deterministically.
	policy, exceeded := budgetedRetryPolicy("boundary", 10*time.Millisecond, 10*time.Millisecond)

	attempts := 0
	_, err := policy.Do(context.Background(), func(_ context.Context) (string, error) {
		attempts++

		return "", Transient(errors.New("down"))
	})

	require.ErrorIs(t, err, ErrTimeBudgetExceeded)
	assert.Equal(t, 1, attempts)
	assert.Equal(t, 1, *exceeded)
}

func TestWithTimeBudgetProceedsWhenAmple(t *testing.T) {
	t.Parallel()

	// A 10s budget dwarfs the 1ms backoff, so all attempts run normally.
	policy, exceeded := budgetedRetryPolicy("ample", 10*time.Second, time.Millisecond)

	attempts := 0
	_, err := policy.Do(context.Background(), func(_ context.Context) (string, error) {
		attempts++

		return "", Transient(errors.New("still failing"))
	})

	require.ErrorIs(t, err, ErrRetriesExhausted)
	assert.NotErrorIs(t, err, ErrTimeBudgetExceeded)
	assert.Equal(t, 5, attempts)
	assert.Zero(t, *exceeded)
	assert.Equal(t, int64(0), policy.Metrics().TimeBudgetExceeded)
}

// ---------------------------------------------------------------------------
// Hedge honors the time budget
// ---------------------------------------------------------------------------

func TestDoHedgeSkipsHedgeWhenBudgetSpent(t *testing.T) {
	t.Parallel()

	clk := newBudgetClock()

	var triggered int

	hooks := &Hooks{OnHedgeTriggered: func() { triggered++ }}

	// Budget exactly spent (deadline == now -> remaining == 0): the hedge timer
	// fires immediately but the hedge must be skipped and the primary returned.
	ctx := attachTimeBudget(context.Background(), clk.Now())

	release := make(chan struct{})

	fn := func(_ context.Context) (string, error) {
		<-release

		return "primary", nil
	}

	type result struct {
		val string
		err error
	}

	resCh := make(chan result, 1)

	go func() {
		v, err := DoHedge[string](ctx, fn, HedgeParams{
			Delay: time.Millisecond,
			Hooks: hooks,
			Clock: clk,
		})
		resCh <- result{val: v, err: err}
	}()

	close(release)

	got := <-resCh
	require.NoError(t, got.err)
	assert.Equal(t, "primary", got.val)
	assert.Zero(t, triggered, "no hedge fires once the budget is spent")
}

func TestDoHedgeBudgetSpentHonorsContextCancel(t *testing.T) {
	t.Parallel()

	// A manually-fired clock lets us pick the timer branch deterministically
	// before cancelling, so the cancellation is observed inside the budget-skip
	// wait rather than racing the outer select.
	clk := newTestClock()

	ctx, cancel := context.WithCancel(attachTimeBudget(context.Background(), clk.Now()))

	// Primary blocks on a channel independent of its context, so cancelling ctx
	// does not let it complete: with the budget spent the hedge is skipped,
	// leaving the caller's cancellation as the only way out.
	block := make(chan struct{})
	defer close(block)

	fn := func(_ context.Context) (string, error) {
		<-block

		return "", nil
	}

	type result struct {
		val string
		err error
	}

	resCh := make(chan result, 1)

	go func() {
		v, err := DoHedge[string](ctx, fn, HedgeParams{
			Delay: time.Millisecond,
			Hooks: &Hooks{},
			Clock: clk,
		})
		resCh <- result{val: v, err: err}
	}()

	// Fire the hedge timer (budget spent -> hedge skipped, waiting for the
	// primary), then cancel so the skip-wait exits via ctx.Done().
	require.Eventually(t, func() bool { return clk.timerCount() >= 1 },
		waitTimeout, waitTick)
	clk.getTimer(0).fire()
	cancel()

	got := <-resCh
	require.ErrorIs(t, got.err, context.Canceled)
}

// ---------------------------------------------------------------------------
// Misconfiguration: time budget with nothing to gate
// ---------------------------------------------------------------------------

func TestWithTimeBudgetWithoutConsumerPanics(t *testing.T) {
	t.Parallel()

	// No retry and no hedge: the budget would gate nothing, so it is rejected.
	assert.PanicsWithValue(t, ErrTimeBudgetWithoutConsumer, func() {
		_ = NewPolicy[string]("p", WithTimeBudget(time.Second))
	})

	// A hedge alone is enough of a consumer.
	assert.NotPanics(t, func() {
		_ = NewPolicy[string]("p", WithHedge(10*time.Millisecond), WithTimeBudget(time.Second))
	})
}

// ---------------------------------------------------------------------------
// Config build and reconfigure
// ---------------------------------------------------------------------------

func testRetryConfig() *RetryConfig {
	backoff := "constant"
	base := "10ms"
	maxAttempts := 5

	return &RetryConfig{Backoff: &backoff, BaseDelay: &base, MaxAttempts: &maxAttempts}
}

func TestBuildOptionsTimeBudgetTakesEffect(t *testing.T) {
	t.Parallel()

	budget := "1ms"

	opts, err := BuildOptions(&PolicyConfig{
		Retry:      testRetryConfig(),
		TimeBudget: &budget,
	})
	require.NoError(t, err)

	// Apply the built options on a frozen clock and confirm the 1ms budget
	// actually stops the 10ms-backoff retry early.
	policy := NewPolicy[string]("cfg", append([]Option{WithClock(newBudgetClock())}, opts...)...)

	attempts := 0
	_, doErr := policy.Do(context.Background(), func(_ context.Context) (string, error) {
		attempts++

		return "", Transient(errors.New("down"))
	})

	require.ErrorIs(t, doErr, ErrTimeBudgetExceeded)
	assert.Equal(t, 1, attempts)
}

func TestBuildOptionsTimeBudgetWithoutConsumer(t *testing.T) {
	t.Parallel()

	budget := "5s"

	_, err := BuildOptions(&PolicyConfig{TimeBudget: &budget})
	require.ErrorIs(t, err, ErrTimeBudgetWithoutConsumer)
	require.ErrorContains(t, err, "time_budget")
}

func TestBuildOptionsTimeBudgetInvalid(t *testing.T) {
	t.Parallel()

	bad := "nope"

	_, err := BuildOptions(&PolicyConfig{Retry: testRetryConfig(), TimeBudget: &bad})
	require.Error(t, err)
	require.ErrorContains(t, err, "time_budget")
}

func TestReconfigureTimeBudgetTakesEffect(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string]("recfg-budget",
		WithClock(newBudgetClock()),
		WithRetry(5, ConstantBackoff(10*time.Millisecond)),
		WithTimeBudget(10*time.Second)) // generous initially

	// Tighten the budget so the 10ms backoff now overruns it.
	budget := "1ms"
	require.NoError(t, policy.Reconfigure(PolicyConfig{TimeBudget: &budget}))

	attempts := 0
	_, err := policy.Do(context.Background(), func(_ context.Context) (string, error) {
		attempts++

		return "", Transient(errors.New("down"))
	})

	require.ErrorIs(t, err, ErrTimeBudgetExceeded)
	assert.Equal(t, 1, attempts, "the tightened budget must take effect")
}

func TestReconfigureTimeBudgetInvalid(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string]("recfg-budget-bad",
		WithRetry(3, ConstantBackoff(time.Millisecond)),
		WithTimeBudget(time.Second))

	bad := "nope"
	err := policy.Reconfigure(PolicyConfig{TimeBudget: &bad})
	require.Error(t, err)
	require.ErrorContains(t, err, "time_budget")
}

func TestReconfigureTimeBudgetAbsent(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string]("no-budget", WithBulkhead(5))

	budget := "2s"
	err := policy.Reconfigure(PolicyConfig{TimeBudget: &budget})
	require.ErrorIs(t, err, ErrPatternAbsent)
}
