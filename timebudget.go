package r8e

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Time budget — a cooperative total deadline shared across retry and hedge,
// optionally propagated downstream as a hard, clock-driven context deadline.
// ---------------------------------------------------------------------------.

type (
	// timeBudgetConfig holds the optional configuration for the time budget.
	timeBudgetConfig struct {
		propagateDeadline bool
	}

	// TimeBudgetOption configures the time budget passed to [WithTimeBudget].
	TimeBudgetOption func(*timeBudgetConfig)

	// timeBudgetState is the reloadable time-budget configuration. It is swapped
	// atomically as one value so a call reads budget and propagateDeadline as a
	// single consistent snapshot, and so the two settings cannot fall out of sync.
	timeBudgetState struct {
		budget            time.Duration
		propagateDeadline bool
	}

	// hardDeadlineParams groups the per-call inputs threaded into the
	// hard-deadline budget path (an unexported parameter bundle).
	hardDeadlineParams struct {
		clock  Clock
		hooks  *Hooks
		budget time.Duration
	}

	// timeBudgetKey is the unexported context key carrying the budget deadline.
	timeBudgetKey struct{}

	// budgetDeadlineCtx is a [context.Context] whose deadline is driven by the
	// policy's injected [Clock] rather than the wall clock. It reports a real
	// [context.Context.Deadline] so downstream callees (gRPC, HTTP) can shed work
	// early, while its cancellation is fired by a [Clock] timer — so a fake clock
	// cancels it deterministically in tests instead of waiting on real wall-clock
	// time. See [WithTimeBudget] with [PropagateDeadline].
	//
	// Pattern: Adapter — bridges a Clock-driven timer to the context.Context
	// interface so a fake clock fires a real downstream deadline deterministically.
	budgetDeadlineCtx struct {
		parent   context.Context //nolint:containedctx // a derived context wraps its parent by design
		err      error
		done     chan struct{}
		deadline time.Time
		mu       sync.Mutex
	}
)

// PropagateDeadline makes the time budget set a real, hard context deadline that
// downstream callees observe. Without it the budget is purely cooperative — it
// gates whether retry and hedge start more work but leaves
// [context.Context.Deadline] unset, so a downstream gRPC or HTTP client cannot
// shed early. With it, the budget middleware derives a context whose
// [context.Context.Deadline] reports the budget instant and whose cancellation
// cancels an in-flight attempt once the budget expires, surfacing
// [ErrTimeBudgetExceeded].
//
// The deadline is driven by the policy's [Clock], not the wall clock, so it
// stays deterministic under a fake test clock. Because a real
// [context.Context] deadline is intrinsically wall-clock, the propagated value
// is only meaningful to real downstream callees when the policy runs on
// [RealClock] (production); under a fake clock the reported deadline is a fake
// instant used solely to drive deterministic cancellation in tests.
func PropagateDeadline() TimeBudgetOption {
	return func(cfg *timeBudgetConfig) {
		cfg.propagateDeadline = true
	}
}

// attachTimeBudget returns ctx carrying an absolute deadline that retry and hedge
// honor as a single total budget for the whole call. The deadline is derived
// from the policy's [Clock] (not a real context deadline), so the budget stays
// deterministic under a fake clock in tests; it is cooperative — it gates
// whether retry and hedge start more work, but it does not cancel an in-flight
// attempt (pair it with [WithTimeout] or PerAttemptTimeout to bound a single
// attempt, or enable [PropagateDeadline] for a hard, clock-driven deadline).
func attachTimeBudget(ctx context.Context, deadline time.Time) context.Context {
	return context.WithValue(ctx, timeBudgetKey{}, deadline)
}

// timeBudgetRemaining reports how much of the ctx time budget is left, measured
// against clock, and whether a budget is present at all. With no budget it
// returns (0, false) so callers proceed without budget gating.
func timeBudgetRemaining(ctx context.Context, clock Clock) (time.Duration, bool) {
	deadline, ok := ctx.Value(timeBudgetKey{}).(time.Time)
	if !ok {
		return 0, false
	}

	return deadline.Sub(clock.Now()), true
}

// applyHardDeadline runs next under the time budget in hard mode: it attaches the
// cooperative deadline value that retry and hedge consult and additionally wraps
// the context in a clock-driven deadline (see [PropagateDeadline]) that
// downstream callees observe and that cancels an in-flight attempt when the
// budget expires. Such a cancellation surfaces as [ErrTimeBudgetExceeded]
// wrapping the downstream error, unifying it with the cooperative retry/hedge
// stop path.
//
//nolint:ireturn // generic type parameter T, not an interface
func applyHardDeadline[T any](
	ctx context.Context,
	next func(context.Context) (T, error),
	params hardDeadlineParams,
) (T, error) {
	deadline := params.clock.Now().Add(params.budget)
	budgetCtx := attachTimeBudget(ctx, deadline)

	hardCtx, cancel := newBudgetDeadlineCtx(budgetCtx, params.clock, deadline)
	defer cancel()

	val, err := next(hardCtx)

	if budgetDeadlineFired(ctx, hardCtx, err) {
		params.hooks.emitTimeBudgetExceeded()

		var zero T

		return zero, fmt.Errorf("%w: %w", ErrTimeBudgetExceeded, err)
	}

	return val, err
}

// budgetDeadlineFired reports whether next failed because THIS budget's
// clock-driven deadline cancelled an in-flight attempt: our own deadline expired
// (hardCtx) while the parent is still live — mirroring DoTimeout's
// timeout-vs-parent-cancel distinction, so a parent cancellation or a tighter
// parent deadline is not misattributed to the budget.
//
// The final clause skips re-attribution when the inner cooperative path already
// raised ErrTimeBudgetExceeded: when the deadline cancels an in-flight attempt,
// retry's own budget check often trips on the same expired budget, so wrapping
// again here would double the sentinel and double-fire the hook and metric.
func budgetDeadlineFired(
	parent context.Context,
	hardCtx *budgetDeadlineCtx,
	err error,
) bool {
	return err != nil &&
		parent.Err() == nil &&
		errors.Is(hardCtx.Err(), context.DeadlineExceeded) &&
		!errors.Is(err, ErrTimeBudgetExceeded)
}

// newBudgetDeadlineCtx derives a clock-driven deadline context from parent that
// expires at deadline. deadline is the single source of truth — the absolute
// instant Deadline reports AND the instant the watcher's timer fires (its
// duration is derived as deadline-now), so the reported and enforced deadlines
// cannot drift apart. The returned cancel func releases the watcher goroutine
// and timer; callers must always invoke it.
func newBudgetDeadlineCtx(
	parent context.Context,
	clock Clock,
	deadline time.Time,
) (*budgetDeadlineCtx, context.CancelFunc) {
	// Honor the tighter of our budget deadline and any parent deadline, so a
	// downstream callee reads the soonest bound (mirrors context.WithDeadline).
	effective := deadline
	if pd, ok := parent.Deadline(); ok && pd.Before(effective) {
		effective = pd
	}

	bctx := &budgetDeadlineCtx{
		parent:   parent,
		done:     make(chan struct{}),
		deadline: effective,
	}

	cancel := func() { bctx.cancel(context.Canceled) }

	// A parent already done, or an already-spent budget (deadline now or past),
	// resolves immediately with no timer or goroutine to manage.
	if err := parent.Err(); err != nil {
		bctx.cancel(err)

		return bctx, cancel
	}

	remaining := deadline.Sub(clock.Now())
	if remaining <= 0 {
		bctx.cancel(context.DeadlineExceeded)

		return bctx, cancel
	}

	go bctx.watch(clock.NewTimer(remaining))

	return bctx, cancel
}

// watch resolves the context when the budget timer fires, the parent is
// cancelled, or the returned cancel func is called, then stops the timer. It is
// a single goroutine that always exits, so it cannot leak.
func (c *budgetDeadlineCtx) watch(timer Timer) {
	select {
	case <-timer.C():
		c.cancel(context.DeadlineExceeded)
	case <-c.parent.Done():
		c.cancel(c.parent.Err())
	case <-c.done:
		// Resolved by an explicit cancel; nothing to record.
	}

	timer.Stop()
}

// cancel resolves the context with err the first time it is called; later calls
// are no-ops, so the first cause (timer, parent, or explicit cancel) wins and
// Err is non-nil once Done is closed.
func (c *budgetDeadlineCtx) cancel(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.err != nil {
		return
	}

	c.err = err
	close(c.done)
}

// Deadline reports the clock-derived budget instant so downstream callees can
// shed work early.
func (c *budgetDeadlineCtx) Deadline() (time.Time, bool) {
	return c.deadline, true
}

// Done returns a channel closed when the budget expires, the parent is
// cancelled, or the context is explicitly cancelled.
func (c *budgetDeadlineCtx) Done() <-chan struct{} {
	return c.done
}

// Err returns the cause once Done is closed: [context.DeadlineExceeded] when the
// budget expired, or the parent/cancellation error otherwise. It returns nil
// while the context is still live.
func (c *budgetDeadlineCtx) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.err
}

// Value delegates to the parent, preserving the cooperative time-budget value
// and any other request-scoped values for the inner chain.
func (c *budgetDeadlineCtx) Value(key any) any {
	return c.parent.Value(key)
}
