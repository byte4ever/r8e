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

// ---------------------------------------------------------------------------
// Hard deadline propagation (C5) — budgetDeadlineCtx, the clock-driven context
// ---------------------------------------------------------------------------

func TestBudgetDeadlineCtxReportsDeadline(t *testing.T) {
	t.Parallel()

	clk := newTestClock()
	want := clk.Now().Add(time.Minute)

	c, cancel := newBudgetDeadlineCtx(context.Background(), clk, want)
	defer cancel()

	got, ok := c.Deadline()
	require.True(t, ok)
	assert.Equal(t, want, got, "Deadline reports the clock-derived budget instant")

	// While live: Err is nil and Done is open.
	require.NoError(t, c.Err())

	select {
	case <-c.Done():
		t.Fatal("Done closed before the budget expired")
	default:
	}
}

func TestBudgetDeadlineCtxTimerFiresDeadlineExceeded(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	c, cancel := newBudgetDeadlineCtx(
		context.Background(), clk, clk.Now().Add(time.Minute),
	)
	defer cancel()

	require.Eventually(t, func() bool { return clk.timerCount() >= 1 },
		waitTimeout, waitTick)
	clk.getTimer(0).fire()

	<-c.Done()
	require.ErrorIs(t, c.Err(), context.DeadlineExceeded)
}

func TestBudgetDeadlineCtxCancelReleases(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	c, cancel := newBudgetDeadlineCtx(
		context.Background(), clk, clk.Now().Add(time.Minute),
	)
	require.NoError(t, c.Err())

	// Explicit cancel resolves the context (exercises watch's <-c.done branch).
	cancel()

	<-c.Done()
	require.ErrorIs(t, c.Err(), context.Canceled)

	// A second cancel is a no-op: the first cause wins.
	cancel()
	require.ErrorIs(t, c.Err(), context.Canceled)
}

func TestBudgetDeadlineCtxParentAlreadyDone(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	parent, pcancel := context.WithCancel(context.Background())
	pcancel() // already cancelled before deriving

	c, cancel := newBudgetDeadlineCtx(parent, clk, clk.Now().Add(time.Minute))
	defer cancel()

	<-c.Done()
	require.ErrorIs(t, c.Err(), context.Canceled)
	assert.Zero(t, clk.timerCount(), "an already-done parent starts no timer")
}

func TestBudgetDeadlineCtxParentCancelPropagates(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	parent, pcancel := context.WithCancel(context.Background())

	c, cancel := newBudgetDeadlineCtx(parent, clk, clk.Now().Add(time.Minute))
	defer cancel()

	require.NoError(t, c.Err()) // live until the parent is cancelled

	pcancel()

	<-c.Done()
	require.ErrorIs(t, c.Err(), context.Canceled)
}

func TestBudgetDeadlineCtxParentDeadlinePropagatesCause(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	// The parent is itself a clock-driven deadline context, so firing its timer
	// resolves it with context.DeadlineExceeded (not Canceled). The child must
	// propagate the parent's actual cause — a regression hardcoding
	// context.Canceled in watch's parent branch would be caught here.
	parent, pcancel := newBudgetDeadlineCtx(
		context.Background(), clk, clk.Now().Add(time.Hour),
	)
	defer pcancel()

	child, ccancel := newBudgetDeadlineCtx(parent, clk, clk.Now().Add(time.Hour))
	defer ccancel()

	require.NoError(t, child.Err()) // live until the parent's deadline fires

	// Two timers exist: parent's (index 0) and child's (index 1). Fire only the
	// parent's so the child resolves via its parent-Done branch, not its own.
	require.Eventually(t, func() bool { return clk.timerCount() >= 2 },
		waitTimeout, waitTick)
	clk.getTimer(0).fire()

	<-child.Done()
	require.ErrorIs(t, child.Err(), context.DeadlineExceeded,
		"the parent's cause propagates, not a hardcoded Canceled")
}

func TestBudgetDeadlineCtxExpiredDeadline(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	// A deadline already in the past is spent: resolve immediately, no timer.
	c, cancel := newBudgetDeadlineCtx(
		context.Background(), clk, clk.Now().Add(-time.Hour),
	)
	defer cancel()

	<-c.Done()
	require.ErrorIs(t, c.Err(), context.DeadlineExceeded)
	assert.Zero(t, clk.timerCount(), "a spent budget starts no timer")
}

func TestBudgetDeadlineCtxHonorsTighterParentDeadline(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	// Parent deadline one second out is tighter than our one-hour budget.
	parentDL := clk.Now().Add(time.Second)
	parent, pcancel := context.WithDeadline(context.Background(), parentDL)
	defer pcancel()

	c, cancel := newBudgetDeadlineCtx(parent, clk, clk.Now().Add(time.Hour))
	defer cancel()

	got, ok := c.Deadline()
	require.True(t, ok)
	assert.Equal(t, parentDL, got, "the soonest of parent and budget deadline wins")
}

func TestBudgetDeadlineCtxValueDelegates(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	deadline := clk.Now().Add(time.Minute)
	parent := attachTimeBudget(context.Background(), deadline)

	c, cancel := newBudgetDeadlineCtx(parent, clk, deadline)
	defer cancel()

	// The cooperative budget value remains reachable through the bridge so the
	// inner retry/hedge still consult it.
	got, ok := c.Value(timeBudgetKey{}).(time.Time)
	require.True(t, ok)
	assert.Equal(t, deadline, got)
}

// ---------------------------------------------------------------------------
// Hard deadline propagation (C5) — wired through Policy.Do
// ---------------------------------------------------------------------------

func TestWithTimeBudgetPropagateExposesDeadline(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	var sawDeadline bool

	policy := NewPolicy[string]("propagate",
		WithClock(clk),
		WithRetry(2, ConstantBackoff(time.Millisecond)),
		WithTimeBudget(time.Hour, PropagateDeadline()),
	)

	val, err := policy.Do(context.Background(),
		func(ctx context.Context) (string, error) {
			_, sawDeadline = ctx.Deadline()

			return "ok", nil
		})
	require.NoError(t, err)
	assert.Equal(t, "ok", val)
	assert.True(t, sawDeadline, "fn must observe the propagated context deadline")
}

func TestWithTimeBudgetNoPropagateLeavesDeadlineUnset(t *testing.T) {
	t.Parallel()

	var sawDeadline bool

	policy := NewPolicy[string]("no-propagate",
		WithClock(newBudgetClock()),
		WithRetry(2, ConstantBackoff(time.Millisecond)),
		WithTimeBudget(time.Hour), // cooperative only
	)

	_, err := policy.Do(context.Background(),
		func(ctx context.Context) (string, error) {
			_, sawDeadline = ctx.Deadline()

			return "ok", nil
		})
	require.NoError(t, err)
	assert.False(t, sawDeadline,
		"without PropagateDeadline the budget sets no context deadline")
}

func TestWithTimeBudgetPropagateCancelsInFlightAttempt(t *testing.T) {
	t.Parallel()

	clk := newTestClock()

	exceeded := 0

	policy := NewPolicy[string]("hard-deadline",
		WithClock(clk),
		WithHooks(&Hooks{OnTimeBudgetExceeded: func() { exceeded++ }}),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
		WithTimeBudget(time.Hour, PropagateDeadline()),
	)

	started := make(chan struct{})

	type result struct {
		val string
		err error
	}

	resCh := make(chan result, 1)

	go func() {
		v, err := policy.Do(context.Background(),
			func(ctx context.Context) (string, error) {
				close(started)
				<-ctx.Done() // block until the budget deadline cancels us

				return "", ctx.Err()
			})
		resCh <- result{val: v, err: err}
	}()

	<-started
	// The budget bridge created its timer before invoking the chain; firing it
	// expires the budget while the attempt is still in flight.
	require.Eventually(t, func() bool { return clk.timerCount() >= 1 },
		waitTimeout, waitTick)
	clk.getTimer(0).fire()

	got := <-resCh
	// The in-flight cancellation surfaces as the unified budget sentinel,
	// wrapping the downstream context.DeadlineExceeded.
	require.ErrorIs(t, got.err, ErrTimeBudgetExceeded)
	require.ErrorIs(t, got.err, context.DeadlineExceeded)
	assert.Equal(t, 1, exceeded)
	assert.Equal(t, int64(1), policy.Metrics().TimeBudgetExceeded)
}

func TestWithTimeBudgetPropagateCountsExceededOnce(t *testing.T) {
	t.Parallel()

	// budget(1ms) << backoff(1h): once the hard deadline cancels the in-flight
	// attempt, retry's own cooperative check also trips on the spent budget. The
	// sentinel must be raised — and the hook/metric counted — exactly once, not
	// doubled by both paths.
	clk := newTestClock()

	exceeded := 0

	policy := NewPolicy[string]("exceeded-once",
		WithClock(clk),
		WithHooks(&Hooks{OnTimeBudgetExceeded: func() { exceeded++ }}),
		WithRetry(3, ConstantBackoff(time.Hour)),
		WithTimeBudget(time.Millisecond, PropagateDeadline()),
	)

	started := make(chan struct{})

	type result struct {
		val string
		err error
	}

	resCh := make(chan result, 1)

	go func() {
		v, err := policy.Do(context.Background(),
			func(ctx context.Context) (string, error) {
				close(started)
				<-ctx.Done()

				return "", ctx.Err()
			})
		resCh <- result{val: v, err: err}
	}()

	<-started
	require.Eventually(t, func() bool { return clk.timerCount() >= 1 },
		waitTimeout, waitTick)
	clk.getTimer(0).fire()

	got := <-resCh
	require.ErrorIs(t, got.err, ErrTimeBudgetExceeded)
	require.ErrorIs(t, got.err, context.DeadlineExceeded)
	assert.Equal(t, 1, exceeded, "the budget sentinel fires exactly once")
	assert.Equal(t, int64(1), policy.Metrics().TimeBudgetExceeded)
}

func TestWithTimeBudgetPropagatePassesThroughOtherErrors(t *testing.T) {
	t.Parallel()

	exceeded := 0
	boom := errors.New("boom")

	policy := NewPolicy[string]("propagate-passthrough",
		WithClock(newTestClock()),
		WithHooks(&Hooks{OnTimeBudgetExceeded: func() { exceeded++ }}),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
		WithTimeBudget(time.Hour, PropagateDeadline()),
	)

	_, err := policy.Do(context.Background(),
		func(_ context.Context) (string, error) {
			return "", Permanent(boom)
		})

	// A non-deadline failure is returned untouched, not re-attributed to the
	// budget.
	require.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, ErrTimeBudgetExceeded)
	assert.Zero(t, exceeded)
	assert.Equal(t, int64(0), policy.Metrics().TimeBudgetExceeded)
}

func TestWithTimeBudgetPropagateDoesNotAttributeParentDeadline(t *testing.T) {
	t.Parallel()

	// The PARENT context is already past its own deadline, so its failure
	// surfaces as context.DeadlineExceeded — the same error our hard deadline
	// would raise. The `parent.Err() == nil` guard must keep this attributed to
	// the parent, NOT re-wrapped as a budget breach (mirrors DoTimeout's
	// parent-cancel distinction). Dropping that guard would ship green without
	// this test.
	exceeded := 0

	policy := NewPolicy[string]("parent-deadline",
		WithClock(newTestClock()),
		WithHooks(&Hooks{OnTimeBudgetExceeded: func() { exceeded++ }}),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
		WithTimeBudget(time.Hour, PropagateDeadline()),
	)

	parent, cancel := context.WithDeadline(
		context.Background(), time.Now().Add(-time.Hour),
	)
	defer cancel()

	_, err := policy.Do(parent, func(ctx context.Context) (string, error) {
		<-ctx.Done()

		return "", ctx.Err()
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotErrorIs(t, err, ErrTimeBudgetExceeded,
		"a parent deadline is not a budget breach")
	assert.Zero(t, exceeded)
	assert.Equal(t, int64(0), policy.Metrics().TimeBudgetExceeded)
}

func TestBuildOptionsPropagateDeadline(t *testing.T) {
	t.Parallel()

	budget := "1h"
	yes := true

	opts, err := BuildOptions(&PolicyConfig{
		Retry:             testRetryConfig(),
		TimeBudget:        &budget,
		PropagateDeadline: &yes,
	})
	require.NoError(t, err)

	var sawDeadline bool

	policy := NewPolicy[string]("cfg-propagate",
		append([]Option{WithClock(newTestClock())}, opts...)...)

	_, doErr := policy.Do(context.Background(),
		func(ctx context.Context) (string, error) {
			_, sawDeadline = ctx.Deadline()

			return "ok", nil
		})
	require.NoError(t, doErr)
	assert.True(t, sawDeadline, "config-built policy propagates the deadline")
}

func TestBuildOptionsPropagateDeadlineWithoutBudget(t *testing.T) {
	t.Parallel()

	yes := true

	_, err := BuildOptions(&PolicyConfig{PropagateDeadline: &yes})
	require.ErrorIs(t, err, ErrDeadlinePropagationWithoutBudget)
	require.ErrorContains(t, err, "propagate_deadline")
}

func TestBuildOptionsPropagateDeadlineFalseIgnored(t *testing.T) {
	t.Parallel()

	no := false

	// Propagation explicitly disabled without a budget is simply ignored.
	opts, err := BuildOptions(&PolicyConfig{PropagateDeadline: &no})
	require.NoError(t, err)
	assert.Empty(t, opts)
}

func TestReconfigurePropagateDeadlineToggles(t *testing.T) {
	t.Parallel()

	policy := NewPolicy[string]("recfg-propagate",
		WithClock(newTestClock()),
		WithRetry(2, ConstantBackoff(time.Millisecond)),
		WithTimeBudget(time.Hour), // propagation off initially
	)

	sawDeadline := func() bool {
		var ok bool

		_, _ = policy.Do(context.Background(),
			func(ctx context.Context) (string, error) {
				_, ok = ctx.Deadline()

				return "ok", nil
			})

		return ok
	}

	require.False(t, sawDeadline(), "starts cooperative-only")

	yes := true
	require.NoError(t, policy.Reconfigure(PolicyConfig{PropagateDeadline: &yes}))
	require.True(t, sawDeadline(), "reconfigure turns propagation on")

	no := false
	require.NoError(t, policy.Reconfigure(PolicyConfig{PropagateDeadline: &no}))
	require.False(t, sawDeadline(), "reconfigure turns propagation back off")
}

func TestReconfigurePropagateDeadlineAbsent(t *testing.T) {
	t.Parallel()

	// No time budget: there is no deadline to derive, so propagation is rejected.
	policy := NewPolicy[string]("no-budget-propagate", WithBulkhead(5))

	yes := true
	err := policy.Reconfigure(PolicyConfig{PropagateDeadline: &yes})
	require.ErrorIs(t, err, ErrDeadlinePropagationWithoutBudget)
}

// ---------------------------------------------------------------------------
// RespectInboundDeadline — the ingress half of cross-service propagation: the
// budget honors a deadline already on the incoming context as a ceiling.
// ---------------------------------------------------------------------------

// inboundDeadlineCtx reports a fixed deadline but, like context.Background,
// never cancels — letting tests drive the inbound-deadline clamp against a
// frozen budgetClock without the wall-clock firing of context.WithDeadline. It
// stores no parent context, so it never trips the containedctx linter.
type inboundDeadlineCtx struct {
	deadline time.Time
}

func (c inboundDeadlineCtx) Deadline() (time.Time, bool) { return c.deadline, true }
func (inboundDeadlineCtx) Done() <-chan struct{}         { return nil }
func (inboundDeadlineCtx) Err() error                    { return nil }
func (inboundDeadlineCtx) Value(any) any                 { return nil }

func TestTightenToInbound(t *testing.T) {
	t.Parallel()

	base := time.Unix(1_700_000_000, 0)
	budget := base.Add(time.Hour)

	t.Run("sooner inbound wins", func(t *testing.T) {
		t.Parallel()

		ctx := inboundDeadlineCtx{deadline: base.Add(time.Minute)}
		assert.Equal(t, base.Add(time.Minute), tightenToInbound(ctx, budget))
	})

	t.Run("later inbound is ignored", func(t *testing.T) {
		t.Parallel()

		ctx := inboundDeadlineCtx{deadline: base.Add(2 * time.Hour)}
		assert.Equal(t, budget, tightenToInbound(ctx, budget))
	})

	t.Run("equal inbound is not tightened", func(t *testing.T) {
		t.Parallel()

		// Before is strict: an inbound deadline equal to the budget leaves it
		// unchanged. Pins the boundary against a `<`->`<=` regression.
		ctx := inboundDeadlineCtx{deadline: budget}
		assert.Equal(t, budget, tightenToInbound(ctx, budget))
	})

	t.Run("no inbound deadline", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, budget, tightenToInbound(context.Background(), budget))
	})
}

func TestWithTimeBudgetRespectInboundClampsCooperativeGate(t *testing.T) {
	t.Parallel()

	clk := newBudgetClock()
	exceeded := 0

	// The configured budget is an hour, but the inbound deadline leaves only
	// 500ms — less than the 1s backoff — so the first retry is suppressed.
	policy := NewPolicy[string]("respect-inbound",
		WithClock(clk),
		WithHooks(&Hooks{OnTimeBudgetExceeded: func() { exceeded++ }}),
		WithRetry(5, ConstantBackoff(time.Second)),
		WithTimeBudget(time.Hour, RespectInboundDeadline()),
	)

	ctx := inboundDeadlineCtx{deadline: clk.Now().Add(500 * time.Millisecond)}

	attempts := 0
	_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		attempts++

		return "", Transient(errors.New("down"))
	})

	require.ErrorIs(t, err, ErrTimeBudgetExceeded)
	assert.Equal(t, 1, attempts,
		"an inbound deadline shorter than the backoff stops the retry")
	assert.Equal(t, 1, exceeded)
}

func TestWithTimeBudgetRespectInboundClampsHardMode(t *testing.T) {
	t.Parallel()

	clk := newTestClock()
	exceeded := 0

	// Both halves on: honor the inbound deadline (ingress) AND propagate a hard
	// deadline (egress) — the headline cross-service use. The inbound 100ms window
	// is shorter than the 1s backoff, so the cooperative gate (fed the SAME
	// clamped deadline as the hard context) suppresses the retry before the hard
	// deadline ever fires.
	policy := NewPolicy[string]("respect-inbound-hard",
		WithClock(clk),
		WithHooks(&Hooks{OnTimeBudgetExceeded: func() { exceeded++ }}),
		WithRetry(5, ConstantBackoff(time.Second)),
		WithTimeBudget(time.Hour, RespectInboundDeadline(), PropagateDeadline()),
	)

	ctx := inboundDeadlineCtx{deadline: clk.Now().Add(100 * time.Millisecond)}

	attempts := 0
	_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		attempts++

		return "", Transient(errors.New("down"))
	})

	require.ErrorIs(t, err, ErrTimeBudgetExceeded)
	assert.Equal(t, 1, attempts)
	assert.Equal(t, 1, exceeded, "the budget-exceeded path fires exactly once")
}

func TestWithTimeBudgetRespectInboundIgnoredWhenOff(t *testing.T) {
	t.Parallel()

	clk := newBudgetClock()
	exceeded := 0

	// Same inbound deadline, but WITHOUT RespectInboundDeadline the gate ignores
	// it: the hour-long configured budget governs, so every attempt runs.
	policy := NewPolicy[string]("ignore-inbound",
		WithClock(clk),
		WithHooks(&Hooks{OnTimeBudgetExceeded: func() { exceeded++ }}),
		WithRetry(2, ConstantBackoff(time.Second)),
		WithTimeBudget(time.Hour),
	)

	ctx := inboundDeadlineCtx{deadline: clk.Now().Add(500 * time.Millisecond)}

	attempts := 0
	_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		attempts++

		return "", Transient(errors.New("down"))
	})

	require.ErrorIs(t, err, ErrRetriesExhausted)
	assert.NotErrorIs(t, err, ErrTimeBudgetExceeded)
	assert.Equal(t, 2, attempts,
		"without RespectInboundDeadline the inbound deadline is ignored")
	assert.Zero(t, exceeded)
}

func TestWithTimeBudgetRespectInboundIgnoresLaterInbound(t *testing.T) {
	t.Parallel()

	clk := newBudgetClock()

	// RespectInboundDeadline is ON, but the inbound deadline (an hour out) is
	// LATER than the 500ms budget, so it never tightens anything: the 1ms backoff
	// fits the budget and all attempts run. Proves the clamp only ever shortens.
	policy := NewPolicy[string]("later-inbound",
		WithClock(clk),
		WithRetry(3, ConstantBackoff(time.Millisecond)),
		WithTimeBudget(500*time.Millisecond, RespectInboundDeadline()),
	)

	ctx := inboundDeadlineCtx{deadline: clk.Now().Add(time.Hour)}

	attempts := 0
	_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		attempts++

		return "", Transient(errors.New("down"))
	})

	require.ErrorIs(t, err, ErrRetriesExhausted)
	assert.Equal(t, 3, attempts)
}

func TestBuildOptionsRespectInboundDeadline(t *testing.T) {
	t.Parallel()

	budget := "5s"
	backoff := "constant"
	baseDelay := "10ms"
	maxAttempts := 5
	on := true

	t.Run("with budget builds a honoring policy", func(t *testing.T) {
		t.Parallel()

		opts, err := BuildOptions(&PolicyConfig{
			TimeBudget:             &budget,
			RespectInboundDeadline: &on,
			Retry: &RetryConfig{
				Backoff:     &backoff,
				BaseDelay:   &baseDelay,
				MaxAttempts: &maxAttempts,
			},
		})
		require.NoError(t, err)
		require.NotEmpty(t, opts)

		// The built option must actually honor an inbound deadline: a 5ms inbound
		// window is shorter than the 10ms backoff, so the retry is suppressed. A
		// config path that dropped RespectInboundDeadline() would run both
		// attempts and never raise the sentinel.
		clk := newBudgetClock()
		p := NewPolicy[string]("built-inbound", append(opts, WithClock(clk))...)

		ctx := inboundDeadlineCtx{deadline: clk.Now().Add(5 * time.Millisecond)}

		attempts := 0
		_, err = p.Do(ctx, func(_ context.Context) (string, error) {
			attempts++

			return "", Transient(errors.New("down"))
		})
		require.ErrorIs(t, err, ErrTimeBudgetExceeded)
		assert.Equal(t, 1, attempts)
	})

	t.Run("without budget is rejected", func(t *testing.T) {
		t.Parallel()

		_, err := BuildOptions(&PolicyConfig{RespectInboundDeadline: &on})
		require.ErrorIs(t, err, ErrInboundDeadlineWithoutBudget)
	})
}

func TestReconfigureRespectInboundDeadline(t *testing.T) {
	t.Parallel()

	clk := newBudgetClock()
	exceeded := 0

	policy := NewPolicy[string]("reconf-inbound",
		WithClock(clk),
		WithHooks(&Hooks{OnTimeBudgetExceeded: func() { exceeded++ }}),
		WithRetry(3, ConstantBackoff(time.Second)),
		WithTimeBudget(time.Hour), // RespectInboundDeadline starts OFF
	)

	run := func() (int, error) {
		ctx := inboundDeadlineCtx{deadline: clk.Now().Add(500 * time.Millisecond)}

		attempts := 0
		_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
			attempts++

			return "", Transient(errors.New("down"))
		})

		return attempts, err
	}

	// OFF: the inbound deadline is ignored, retries run to exhaustion.
	attempts, err := run()
	require.ErrorIs(t, err, ErrRetriesExhausted)
	assert.Equal(t, 3, attempts)

	// Turn it ON via Reconfigure — the inbound deadline now gates the retry.
	on := true
	require.NoError(t, policy.Reconfigure(PolicyConfig{RespectInboundDeadline: &on}))

	attempts, err = run()
	require.ErrorIs(t, err, ErrTimeBudgetExceeded)
	assert.Equal(t, 1, attempts)
	assert.Positive(t, exceeded)

	// Turn it back OFF — the inbound deadline is ignored again.
	off := false
	require.NoError(t, policy.Reconfigure(PolicyConfig{RespectInboundDeadline: &off}))

	attempts, err = run()
	require.ErrorIs(t, err, ErrRetriesExhausted)
	assert.Equal(t, 3, attempts)
}

func TestReconfigureRespectInboundDeadlineAbsent(t *testing.T) {
	t.Parallel()

	// No time budget: there is no budget to tighten, so the flag is rejected.
	policy := NewPolicy[string]("no-budget-inbound", WithBulkhead(5))

	on := true
	err := policy.Reconfigure(PolicyConfig{RespectInboundDeadline: &on})
	require.ErrorIs(t, err, ErrInboundDeadlineWithoutBudget)
}
