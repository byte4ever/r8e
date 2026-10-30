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
		propagateDeadline      bool
		respectInboundDeadline bool
	}

	// TimeBudgetOption configures the time budget passed to [WithTimeBudget].
	//
	// Pattern: Functional Options — composable optional settings applied to the
	// private config, keeping [WithTimeBudget]'s signature stable.
	TimeBudgetOption func(*timeBudgetConfig)

	// timeBudgetState is the reloadable time-budget configuration. It is swapped
	// atomically as one value so a call reads budget, propagateDeadline, and
	// respectInboundDeadline as a single consistent snapshot, and so the
	// settings cannot fall out of sync.
	timeBudgetState struct {
		budget                 time.Duration
		propagateDeadline      bool
		respectInboundDeadline bool
	}

	// hardDeadlineParams groups the per-call inputs threaded into the
	// hard-deadline budget path (an unexported parameter bundle). deadline is
	// the budget instant already computed (and, with [RespectInboundDeadline],
	// clamped to any inbound deadline) by the budget middleware.
	hardDeadlineParams struct {
		clock    Clock
		hooks    *Hooks
		deadline time.Time
	}

	// timeBudgetKey is the unexported context key carrying the budget deadline.
	timeBudgetKey struct{}

	// budgetDeadlineCtx is a [context.Context] whose deadline is driven by the
	// policy's injected [Clock] rather than the wall clock. It reports a real
	// [context.Context.Deadline] so downstream callees (gRPC, HTTP) can shed work
	// early, while its cancellation is fired by a [Clock] timer when the deadline
	// is the budget's own — so a fake clock cancels it deterministically in tests
	// instead of waiting on real wall-clock time. A deadline that is the parent's
	// arms no timer. See [WithTimeBudget] with [PropagateDeadline].
	//
	// Pattern: Adapter — bridges a Clock-driven timer to the context.Context
	// interface so a fake clock fires a real downstream deadline deterministically.
	budgetDeadlineCtx struct {
		parent   context.Context //nolint:containedctx // a derived context wraps its parent by design
		err      error
		done     chan struct{}
		deadline time.Time
		mu       sync.Mutex
		// ownDeadline is true when the budget deadline is earlier than the
		// parent's, so it is a deadline r8e set rather than one it adopted
		// from the parent (see RespectInboundDeadline).
		ownDeadline bool
		// cut is true when the budget's own deadline ended the context:
		// budgetDeadlineCtx.settle found the parent live, or past its own,
		// later deadline. It is set once, under mu, when the context is
		// resolved, and decides who owns the call (see budgetDeadlineFired).
		cut bool
	}
)

// PropagateDeadline makes the time budget set a real, hard context deadline that
// downstream callees observe. Without it the budget is purely cooperative — it
// gates whether retry and hedge start more work but leaves
// [context.Context.Deadline] unset, so a downstream gRPC or HTTP client cannot
// shed early. With it, the budget middleware derives a context whose
// [context.Context.Deadline] reports the budget instant and whose cancellation
// cancels an in-flight attempt once the budget expires, surfacing
// [ErrTimeBudgetExceeded] when the deadline is the budget's own, strictly
// earlier than any deadline the context already has. A deadline that is the
// caller's (it comes first or on the same instant, including one adopted
// through [RespectInboundDeadline]) arms no budget timer: a call that the
// caller's context ends returns that context's error, without
// OnTimeBudgetExceeded. Before that, retry still refuses an attempt that
// cannot fit, with ErrTimeBudgetExceeded (see [RespectInboundDeadline]).
//
// Like [DoTimeout], the budget decides who ended the call when its own
// deadline fires: a caller that had already cancelled keeps the call and
// gets its own context error. A caller whose own, later deadline has also
// passed by then does not: the budget's deadline came first. A caller that
// cancels later, while the cut attempt is still returning, does not change
// the decision either.
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

// RespectInboundDeadline makes the time budget honor a deadline already present
// on the incoming context as a ceiling: the effective budget becomes the SOONER
// of clock.Now()+budget and that inbound deadline. This is the ingress half of
// cross-service deadline propagation — a server that received a deadline from
// its caller never lets its own budget run past it ("the smallest deadline
// wins", as in gRPC). Without it the budget is a fixed duration regardless of
// any inbound deadline.
//
// It tightens the budget that retry and hedge consult (the cooperative gate):
// retry does not start an attempt that cannot fit before the inbound deadline
// and returns ErrTimeBudgetExceeded, and hedge fires no hedge once the budget
// is spent. It never extends the budget beyond the configured duration.
// Pair it with [PropagateDeadline] (egress) so a middle service both honors the
// upstream deadline and re-emits the tightened value to its own downstream
// callees. Like the budget itself the inbound deadline is read against the
// policy's [Clock], so it is meaningful to a real downstream caller only on
// [RealClock]; see [PropagateDeadline] for the same fake-clock caveat.
func RespectInboundDeadline() TimeBudgetOption {
	return func(cfg *timeBudgetConfig) {
		cfg.respectInboundDeadline = true
	}
}

// tightenToInbound returns the SOONER of deadline and any deadline already
// present on ctx, implementing the ingress half of cross-service propagation —
// a server never runs its budget past the deadline its caller propagated (see
// [RespectInboundDeadline]). A ctx with no deadline, or one later than
// deadline, returns deadline unchanged, so the result can only ever be sooner,
// never later.
func tightenToInbound(ctx context.Context, deadline time.Time) time.Time {
	if inbound, ok := ctx.Deadline(); ok && inbound.Before(deadline) {
		return inbound
	}

	return deadline
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
// wrapping the error the inner chain returned, as retry's cooperative refusal
// does. A failed call the caller's context ended returns that context's error
// instead (see callerEndedTheCall).
//
// A downstream can stop on the propagated deadline by itself (a socket
// deadline, a gRPC or HTTP client) and fail before the watcher has run. A
// call that returns once the budget's deadline has passed therefore settles
// the context before the attribution, as the watcher would have. For a
// deadline that is the parent's, settle only records an end the parent has
// already reported.
//
//nolint:ireturn // generic type parameter T, not an interface
func applyHardDeadline[T any](
	ctx context.Context,
	next func(context.Context) (T, error),
	params hardDeadlineParams,
) (T, error) {
	budgetCtx := attachTimeBudget(ctx, params.deadline)

	hardCtx, cancel := newBudgetDeadlineCtx(budgetCtx, params.clock, params.deadline)
	defer cancel()

	val, err := next(hardCtx)

	if !params.clock.Now().Before(params.deadline) {
		hardCtx.settle()
	}

	if budgetDeadlineFired(hardCtx, err) {
		params.hooks.emitTimeBudgetExceeded()

		var zero T

		return zero, fmt.Errorf("%w: %w", ErrTimeBudgetExceeded, err)
	}

	if callerEndedTheCall(ctx, hardCtx, err) {
		var zero T

		return zero, ctx.Err() //nolint:wrapcheck // the caller's own context error
	}

	return val, err
}

// budgetDeadlineFired reports whether next failed because THIS budget's own
// deadline cut the call: hardCtx records whether its own deadline ended the
// context (see budgetDeadlineCtx.settle). The decision is made once, the
// first time the budget's deadline or the parent's end is observed, as
// DoTimeout decides when its timeout fires: a parent that cancels afterwards,
// while the cut attempt is still returning, does not take the call back. A
// parent found cancelled, or a deadline that is the parent's (it arms no
// budget timer, see newBudgetDeadlineCtx), leaves the call to the parent (see
// callerEndedTheCall).
//
// The final clause skips re-attribution when retry's cooperative check already
// raised ErrTimeBudgetExceeded and fired the hook: retry refused an attempt
// while the budget context was still live, and the budget timer fired before
// applyHardDeadline checked. Without the clause, such a call would wrap the
// sentinel twice and fire OnTimeBudgetExceeded twice. When the deadline cuts
// an attempt in flight, retry raises no sentinel: it returns an error that is
// not ErrTimeBudgetExceeded (the context's end, the attempt's own error, or
// ErrRetriesExhausted wrapping it), and this function attributes any such
// error.
func budgetDeadlineFired(hardCtx *budgetDeadlineCtx, err error) bool {
	return err != nil &&
		hardCtx.cutByBudget() &&
		!errors.Is(err, ErrTimeBudgetExceeded)
}

// callerEndedTheCall reports whether a failed call is the caller's: the
// caller's context has ended and the budget's own deadline did not cut the
// call first. hardCtx is nil for a cooperative budget, which sets no deadline
// of its own. The call then returns the caller's context error, as DoTimeout
// does, whatever error the inner chain reported once its context ended (a
// downstream's own deadline error, or ErrRetriesExhausted on retry's last
// attempt). An error that already carries ErrTimeBudgetExceeded, from retry's
// cooperative refusal, keeps it.
func callerEndedTheCall(
	parent context.Context,
	hardCtx *budgetDeadlineCtx,
	err error,
) bool {
	return err != nil &&
		parent.Err() != nil &&
		(hardCtx == nil || !hardCtx.cutByBudget()) &&
		!errors.Is(err, ErrTimeBudgetExceeded)
}

// applyCooperativeBudget runs next under the time budget in cooperative mode:
// it attaches the budget value retry and hedge consult and sets no deadline.
// A failed call the caller's context ended returns that context's error, as
// in hard mode (see callerEndedTheCall).
//
//nolint:ireturn // generic type parameter T, not an interface
func applyCooperativeBudget[T any](
	ctx context.Context,
	next func(context.Context) (T, error),
	deadline time.Time,
) (T, error) {
	val, err := next(attachTimeBudget(ctx, deadline))

	if callerEndedTheCall(ctx, nil, err) {
		var zero T

		return zero, ctx.Err() //nolint:wrapcheck // the caller's own context error
	}

	return val, err
}

// newBudgetDeadlineCtx derives a clock-driven deadline context from parent that
// expires at deadline. deadline is the single source of truth — the absolute
// instant Deadline reports AND the instant the watcher's timer fires (its
// duration is derived as deadline-now), so the reported and enforced deadlines
// cannot drift apart. A deadline that does not bind (see deadlineBinds) is the
// parent's: the context then reports the parent's deadline, arms no timer and
// ends only with the parent or the cancel func. The returned cancel func
// releases the watcher goroutine and timer; callers must always invoke it.
func newBudgetDeadlineCtx(
	parent context.Context,
	clock Clock,
	deadline time.Time,
) (*budgetDeadlineCtx, context.CancelFunc) {
	// Honor the tighter of our budget deadline and any parent deadline, so a
	// downstream callee reads the soonest bound (mirrors context.WithDeadline).
	// A budget that does not end strictly before the parent ends on the
	// parent's deadline, so it is not the budget's own.
	effective := deadline
	ownDeadline := deadlineBinds(parent, deadline)

	if !ownDeadline {
		effective, _ = parent.Deadline() // set, since the deadline does not bind
	}

	bctx := &budgetDeadlineCtx{
		parent:      parent,
		done:        make(chan struct{}),
		deadline:    effective,
		ownDeadline: ownDeadline,
	}

	cancel := func() { bctx.cancel(context.Canceled) }

	// A parent already done resolves immediately, with no timer or goroutine
	// to manage. settle decides as the watcher would: the budget instant was
	// taken before this check, so a parent found past a later deadline of its
	// own reached it after the budget's.
	if parent.Err() != nil {
		bctx.settle()

		return bctx, cancel
	}

	if !ownDeadline {
		// The deadline is the parent's: arm no timer and follow the parent
		// alone, so the call ends when the caller's context does and that
		// context already reports its end, as withPolicyTimeout does.
		go bctx.watch(nil)

		return bctx, cancel
	}

	// A budget of its own that is already spent (deadline now or past)
	// resolves immediately too.
	remaining := deadline.Sub(clock.Now())
	if remaining <= 0 {
		bctx.settle()

		return bctx, cancel
	}

	go bctx.watch(clock.NewTimer(remaining))

	return bctx, cancel
}

// watch resolves the context when the budget timer fires, the parent ends, or
// the returned cancel func is called, then stops the timer. The timer and the
// parent take the same path: when both are ready, select picks either one, and
// settle decides from the parent's state, not from the arm. A nil timer (a
// deadline that is the parent's) leaves only the parent and the cancel func.
// It is a single goroutine that always exits, so it cannot leak.
func (c *budgetDeadlineCtx) watch(timer Timer) {
	var fired <-chan time.Time
	if timer != nil {
		fired = timer.C()
		defer timer.Stop()
	}

	select {
	case <-fired:
		c.settle()
	case <-c.parent.Done():
		c.settle()
	case <-c.done:
		// Resolved by an explicit cancel; nothing to record.
	}
}

// cancel resolves the context with err and no cut, unless it is already
// resolved. Only the returned cancel func uses it: the budget's deadline and
// the parent's end go through settle.
func (c *budgetDeadlineCtx) cancel(err error) {
	c.resolve(err, false)
}

// settle resolves the context once the budget's own deadline or the parent's
// end is observed, and decides who ended the call as DoTimeout does. The
// watcher calls it, and so do newBudgetDeadlineCtx, for a parent already done
// or a budget already spent, and applyHardDeadline, for a call that returns
// after the deadline. A deadline that is the parent's (the budget then arms
// no timer) ends only with the parent: while the parent is live, settle
// leaves the context unresolved. A parent found cancelled keeps the call, and
// so does a parent whose deadline is the one the context reports: the
// context takes the parent's error. Any other end is the budget's. Its own
// deadline comes strictly before the parent's, so a parent found past its
// deadline reached it after the budget's: the context ends with
// [context.DeadlineExceeded] and cut records that the budget ended the call.
func (c *budgetDeadlineCtx) settle() {
	err := c.parent.Err()
	if err == nil && !c.ownDeadline {
		// The deadline is the parent's, and the parent has not ended yet.
		return
	}

	if err != nil &&
		(!c.ownDeadline || !errors.Is(err, context.DeadlineExceeded)) {
		c.resolve(err, false)

		return
	}

	c.resolve(context.DeadlineExceeded, true)
}

// resolve sets the context's error and whether the budget cut the call, the
// first time it is called. Every cause goes through it (settle for the
// budget's deadline and the parent's end, cancel for the returned cancel
// func), so the first cause wins, later calls are no-ops and Err is non-nil
// once Done is closed.
func (c *budgetDeadlineCtx) resolve(err error, cut bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.err != nil {
		return
	}

	c.err = err
	c.cut = cut
	close(c.done)
}

// cutByBudget reports whether the budget's own deadline ended the context (see
// settle).
func (c *budgetDeadlineCtx) cutByBudget() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.cut
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
// and any other request-scoped values for the inner chain. The one exception is
// policyDeadlineKey: a budget whose deadline is its own answers it with that
// deadline, so IsCallerCancellation recognises it as r8e's (see withPolicyTimeout).
func (c *budgetDeadlineCtx) Value(key any) any {
	if _, ok := key.(policyDeadlineKey); ok && c.ownDeadline {
		return c.deadline
	}

	return c.parent.Value(key)
}
