package r8e

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Time budget — a cooperative total deadline shared across retry and hedge
// ---------------------------------------------------------------------------.

// timeBudgetKey is the unexported context key carrying the time-budget deadline.
type timeBudgetKey struct{}

// attachTimeBudget returns ctx carrying an absolute deadline that retry and hedge
// honor as a single total budget for the whole call. The deadline is derived
// from the policy's [Clock] (not a real context deadline), so the budget stays
// deterministic under a fake clock in tests; it is cooperative — it gates
// whether retry and hedge start more work, but it does not cancel an in-flight
// attempt (pair it with [WithTimeout] or PerAttemptTimeout to bound a single
// attempt).
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
