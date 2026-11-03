package r8e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// errRuleOwn is the downstream's own error in the shared-rule test.
var errRuleOwn = errors.New("downstream's own error")

// ruleRun runs fn under one of the three patterns that apply the deadline
// rule.
type ruleRun func(ctx context.Context, fn func(context.Context) (string, error)) (string, error)

// ruleOutcome names who ended a call, so the three patterns can be compared
// although each reports r8e's cut with its own sentinel.
func ruleOutcome(val string, err error) string {
	switch {
	case err == nil:
		return "success " + val
	case errors.Is(err, ErrTimeout), errors.Is(err, ErrTimeBudgetExceeded):
		return "cut by r8e"
	case errors.Is(err, errRuleOwn):
		return "downstream's error"
	case errors.Is(err, context.Canceled):
		return "caller cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "caller's deadline"
	default:
		return "unexpected: " + err.Error()
	}
}

// DoTimeout, WithTimeout in a policy and a hard time budget decide who ended
// a call with one rule (cutByOwnDeadline). On the real clock they give the
// same outcome for every case, except a success that comes back after r8e's
// deadline: the timeouts have already returned ErrTimeout, while the budget
// waits for the chain and keeps the success.
func TestDeadlineRuleSharedByTheThreePatterns(t *testing.T) {
	t.Parallel()

	const (
		deadline = 30 * time.Millisecond
		earlier  = 5 * time.Millisecond
		runs     = 10
	)

	patterns := map[string]ruleRun{
		"DoTimeout": func(ctx context.Context, fn func(context.Context) (string, error)) (string, error) {
			return DoTimeout(ctx, deadline, fn, nil)
		},
		"WithTimeout": func(ctx context.Context, fn func(context.Context) (string, error)) (string, error) {
			return NewPolicy[string]("", WithTimeout(deadline)).Do(ctx, fn)
		},
		"WithTimeBudget": func(ctx context.Context, fn func(context.Context) (string, error)) (string, error) {
			return NewPolicy[string]("", WithRetry(1, ConstantBackoff(0)),
				WithTimeBudget(deadline, PropagateDeadline())).Do(ctx, fn)
		},
	}

	// ownErrorOnEnd waits for its context to end, then returns its own error,
	// as a downstream that stops on the propagated deadline does.
	ownErrorOnEnd := func(ctx context.Context) (string, error) {
		<-ctx.Done()

		return "", errRuleOwn
	}

	tests := map[string]struct {
		call   func(run ruleRun) (string, error)
		want   string
		budget string // the budget's outcome when it differs from want
	}{
		"caller cancels before": {
			call: func(run ruleRun) (string, error) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				time.AfterFunc(earlier, cancel)

				return run(ctx, ownErrorOnEnd)
			},
			want: "caller cancelled",
		},
		"downstream error on the caller's earlier deadline": {
			call: func(run ruleRun) (string, error) {
				ctx, cancel := context.WithTimeout(context.Background(), earlier)
				defer cancel()

				return run(ctx, ownErrorOnEnd)
			},
			want: "caller's deadline",
		},
		"downstream error on r8e's binding deadline": {
			call: func(run ruleRun) (string, error) {
				return run(context.Background(), ownErrorOnEnd)
			},
			want: "cut by r8e",
		},
		"success after r8e's deadline": {
			call: func(run ruleRun) (string, error) {
				return run(context.Background(), func(context.Context) (string, error) {
					time.Sleep(2 * deadline)

					return "late", nil
				})
			},
			want:   "cut by r8e",
			budget: "success late",
		},
	}

	for name, tt := range tests {
		for pattern, run := range patterns {
			t.Run(name+"/"+pattern, func(t *testing.T) {
				t.Parallel()

				want := tt.want
				if pattern == "WithTimeBudget" && tt.budget != "" {
					want = tt.budget
				}

				for range runs {
					require.Equal(t, want, ruleOutcome(tt.call(run)))
				}
			})
		}
	}
}

// cutByOwnDeadline: a deadline that does not bind never cuts, a cancelled
// caller keeps the call, a caller past its own (later) deadline loses it,
// and a live caller leaves the decision to the clock.
func TestCutByOwnDeadline(t *testing.T) {
	t.Parallel()

	now := time.Now()
	deadline := now.Add(-2 * time.Hour)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	expired, cancelExpired := context.WithDeadline(context.Background(), now.Add(-time.Hour))
	t.Cleanup(cancelExpired)

	earlierParent, cancelEarlier := context.WithDeadline(context.Background(), deadline.Add(-time.Hour))
	t.Cleanup(cancelEarlier)

	tests := map[string]struct {
		parent context.Context
		now    time.Time
		want   bool
	}{
		"not binding":              {parent: earlierParent, now: now},
		"caller cancelled":         {parent: cancelled, now: now},
		"caller past its deadline": {parent: expired, now: deadline.Add(-time.Second), want: true},
		"live caller, due":         {parent: context.Background(), now: deadline, want: true},
		"live caller, not due":     {parent: context.Background(), now: deadline.Add(-time.Nanosecond)},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, cutByOwnDeadline(tt.parent, deadline, tt.now))
		})
	}
}
