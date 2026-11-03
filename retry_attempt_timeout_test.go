package r8e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attemptRecorder collects the attempts OnAttemptTimeout reports.
type attemptRecorder struct {
	attempts []int
	mu       sync.Mutex
}

func (r *attemptRecorder) hooks() *Hooks {
	return &Hooks{OnAttemptTimeout: func(attempt int) {
		r.mu.Lock()
		defer r.mu.Unlock()

		r.attempts = append(r.attempts, attempt)
	}}
}

func (r *attemptRecorder) got() []int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]int(nil), r.attempts...)
}

// Attempts cut by PerAttemptTimeout carry ErrAttemptTimeout around their own
// error, and OnAttemptTimeout fires once per cut attempt, 1-indexed.
func TestPerAttemptTimeoutWrapsErrAttemptTimeout(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var rec attemptRecorder

		_, err := DoRetry[string](t.Context(), blockUntilDone, RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(time.Millisecond),
			Hooks:       rec.hooks(),
			Clock:       RealClock{},
			Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
		})

		require.ErrorIs(t, err, ErrRetriesExhausted)
		require.ErrorIs(t, err, ErrAttemptTimeout)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.ErrorContains(t, err, "attempt timeout")
		assert.ErrorContains(t, err, "deadline exceeded")
		assert.Equal(t, "attempt_timeout", ErrorCode(err))
		assert.Equal(t, []int{1, 2, 3}, rec.got())
	})
}

// A downstream that stops on the propagated deadline by itself, with its own
// error and before the attempt context reports the deadline, is still cut by
// the attempt timeout.
func TestPerAttemptTimeoutAttributesAnAttemptEndingOnItsDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var rec attemptRecorder

		socketErr := errors.New("socket deadline")

		_, err := DoRetry[string](t.Context(), func(ctx context.Context) (string, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			time.Sleep(time.Until(deadline))

			return "", socketErr
		}, RetryParams{
			MaxAttempts: 1,
			Strategy:    ConstantBackoff(0),
			Hooks:       rec.hooks(),
			Clock:       RealClock{},
			Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
		})

		require.ErrorIs(t, err, ErrAttemptTimeout)
		require.ErrorIs(t, err, socketErr)
		require.NotErrorIs(t, err, context.DeadlineExceeded, "the wrap keeps fn's own error, nothing more")
		assert.Equal(t, []int{1}, rec.got())
	})
}

// A deadline that is not the attempt's own carries no ErrAttemptTimeout: the
// caller's shorter deadline, or an enclosing WithTimeout that ends first.
func TestPerAttemptTimeoutLeavesOtherDeadlinesAlone(t *testing.T) {
	t.Parallel()

	t.Run("caller deadline first", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var rec attemptRecorder

			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
			defer cancel()

			_, err := DoRetry[string](ctx, blockUntilDone, RetryParams{
				MaxAttempts: 1,
				Strategy:    ConstantBackoff(time.Millisecond),
				Hooks:       rec.hooks(),
				Clock:       RealClock{},
				Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
			})

			require.ErrorIs(t, err, ErrRetriesExhausted, "the attempt's own error is returned")
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.NotErrorIs(t, err, ErrAttemptTimeout)
			assert.Empty(t, rec.got())
		})
	})

	t.Run("caller cancellation", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var rec attemptRecorder

			ctx, cancel := context.WithCancel(t.Context())
			time.AfterFunc(5*time.Millisecond, cancel)

			_, err := DoRetry[string](ctx, blockUntilDone, RetryParams{
				MaxAttempts: 1,
				Strategy:    ConstantBackoff(time.Millisecond),
				Hooks:       rec.hooks(),
				Clock:       RealClock{},
				Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
			})

			require.ErrorIs(t, err, ErrRetriesExhausted, "the attempt's own error is returned")
			require.ErrorIs(t, err, context.Canceled)
			require.NotErrorIs(t, err, ErrAttemptTimeout)
			assert.Empty(t, rec.got())
		})
	})

	t.Run("enclosing WithTimeout first", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var rec attemptRecorder

			policy := NewPolicy[string]("",
				WithTimeout(5*time.Millisecond),
				WithRetry(3, ConstantBackoff(time.Millisecond),
					PerAttemptTimeout(10*time.Millisecond)),
				WithHooks(rec.hooks()),
			)

			_, err := policy.Do(t.Context(), blockUntilDone)

			require.ErrorIs(t, err, ErrTimeout)
			require.NotErrorIs(t, err, ErrAttemptTimeout)
			assert.Empty(t, rec.got())
		})
	})

	t.Run("time budget first", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var rec attemptRecorder

			policy := NewPolicy[string]("",
				WithTimeBudget(5*time.Millisecond, PropagateDeadline()),
				WithRetry(1, ConstantBackoff(time.Millisecond),
					PerAttemptTimeout(10*time.Millisecond)),
				WithHooks(rec.hooks()),
			)

			_, err := policy.Do(t.Context(), blockUntilDone)

			require.ErrorIs(t, err, ErrTimeBudgetExceeded)
			require.NotErrorIs(t, err, ErrAttemptTimeout)
			assert.Empty(t, rec.got())
			assert.Equal(t, int64(0), policy.Metrics().AttemptTimeouts)
		})
	})
}

// An attempt that fails with its own error before its deadline is not cut:
// the error comes back unwrapped and no hook fires, with or without a caller
// deadline, and through a policy the metric stays at zero.
func TestPerAttemptTimeoutLeavesAnEarlyFailureAlone(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	failFast := func(context.Context) (string, error) { return "", errBoom }

	tests := map[string]time.Duration{
		"no caller deadline":      0,
		"shorter caller deadline": 5 * time.Millisecond,
	}

	for name, callerTimeout := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var rec attemptRecorder

				ctx := t.Context()

				if callerTimeout > 0 {
					var cancel context.CancelFunc

					ctx, cancel = context.WithTimeout(ctx, callerTimeout)
					defer cancel()
				}

				_, err := DoRetry[string](ctx, failFast, RetryParams{
					MaxAttempts: 1,
					Strategy:    ConstantBackoff(0),
					Hooks:       rec.hooks(),
					Clock:       RealClock{},
					Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
				})

				require.ErrorIs(t, err, errBoom)
				require.NotErrorIs(t, err, ErrAttemptTimeout)
				assert.Empty(t, rec.got())
			})
		})
	}

	t.Run("through a policy", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			policy := NewPolicy[string]("",
				WithRetry(2, ConstantBackoff(time.Millisecond),
					PerAttemptTimeout(10*time.Millisecond)),
			)

			_, err := policy.Do(t.Context(), failFast)

			require.ErrorIs(t, err, errBoom)
			require.NotErrorIs(t, err, ErrAttemptTimeout)
			assert.Equal(t, int64(0), policy.Metrics().AttemptTimeouts)
		})
	})
}

// The cut is decided when the attempt deadline fires. What happens to the
// caller while the cut attempt is still returning does not take it back: a
// later caller deadline, a cancellation, or a time budget that expires.
func TestPerAttemptTimeoutDecidesAtTheCut(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")

	// unwindSlowly stops on its context, then needs 10ms to return.
	unwindSlowly := func(ctx context.Context) (string, error) {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond)

		return "", errBoom
	}

	// ignoreContext returns its own error at 30ms, whatever its context does.
	ignoreContext := func(context.Context) (string, error) {
		time.Sleep(30 * time.Millisecond)

		return "", errBoom
	}

	tests := map[string]struct {
		caller func(context.Context) (context.Context, context.CancelFunc)
		fn     func(context.Context) (string, error)
	}{
		"later caller deadline passes during the unwind": {
			caller: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, 15*time.Millisecond)
			},
			fn: unwindSlowly,
		},
		"caller cancels during the unwind": {
			caller: func(ctx context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(ctx)
				time.AfterFunc(12*time.Millisecond, cancel)

				return ctx, cancel
			},
			fn: unwindSlowly,
		},
		"fn ignores its context past a later caller deadline": {
			caller: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, 20*time.Millisecond)
			},
			fn: ignoreContext,
		},
		"detached parent that keeps an r8e deadline value": {
			caller: func(ctx context.Context) (context.Context, context.CancelFunc) {
				withValue, cancel := withPolicyTimeout(ctx, 10*time.Millisecond)

				return context.WithoutCancel(withValue), cancel
			},
			fn: blockUntilDone,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var rec attemptRecorder

				ctx, cancel := tt.caller(t.Context())
				defer cancel()

				_, err := DoRetry[string](ctx, tt.fn, RetryParams{
					MaxAttempts: 1,
					Strategy:    ConstantBackoff(0),
					Hooks:       rec.hooks(),
					Clock:       RealClock{},
					Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
				})

				require.ErrorIs(t, err, ErrAttemptTimeout)
				assert.Equal(t, []int{1}, rec.got())
			})
		})
	}

	t.Run("time budget expires during the unwind", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var rec attemptRecorder

			policy := NewPolicy[string]("",
				WithTimeBudget(15*time.Millisecond, PropagateDeadline()),
				WithRetry(1, ConstantBackoff(time.Millisecond),
					PerAttemptTimeout(10*time.Millisecond)),
				WithHooks(rec.hooks()),
			)

			_, err := policy.Do(t.Context(), unwindSlowly)

			require.ErrorIs(t, err, ErrAttemptTimeout)
			require.ErrorIs(t, err, errBoom)
			assert.Equal(t, []int{1}, rec.got())
			assert.Equal(t, int64(1), policy.Metrics().AttemptTimeouts)
		})
	})
}

// A cut attempt that returns a Permanent error is wrapped too, still stops
// the retry, and reports the attempt_timeout code.
func TestPerAttemptTimeoutWrapsALatePermanentError(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var rec attemptRecorder

		calls := 0

		_, err := DoRetry[string](t.Context(), func(context.Context) (string, error) {
			calls++

			time.Sleep(20 * time.Millisecond)

			return "", Permanent(ErrCircuitOpen)
		}, RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(0),
			Hooks:       rec.hooks(),
			Clock:       RealClock{},
			Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
		})

		assert.Equal(t, 1, calls, "Permanent still stops the retry")
		require.ErrorIs(t, err, ErrAttemptTimeout)
		require.ErrorIs(t, err, ErrCircuitOpen)
		assert.True(t, IsPermanent(err))
		assert.Equal(t, "attempt_timeout", ErrorCode(err))
		assert.Equal(t, []int{1}, rec.got())
	})
}

// An attempt that succeeds after its deadline passed, ignoring its context,
// keeps its success.
func TestPerAttemptTimeoutKeepsALateSuccess(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var rec attemptRecorder

		result, err := DoRetry[string](t.Context(), func(context.Context) (string, error) {
			time.Sleep(20 * time.Millisecond)

			return "late", nil
		}, RetryParams{
			MaxAttempts: 2,
			Strategy:    ConstantBackoff(0),
			Hooks:       rec.hooks(),
			Clock:       RealClock{},
			Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
		})

		require.NoError(t, err)
		assert.Equal(t, "late", result)
		assert.Empty(t, rec.got())
	})
}

// Through a policy, each cut attempt is counted in AttemptTimeouts and the
// caller's own OnAttemptTimeout still fires.
func TestPolicyCountsAttemptTimeouts(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var rec attemptRecorder

		policy := NewPolicy[string]("",
			WithRetry(2, ConstantBackoff(time.Millisecond),
				PerAttemptTimeout(10*time.Millisecond)),
			WithHooks(rec.hooks()),
		)

		_, err := policy.Do(t.Context(), blockUntilDone)

		require.ErrorIs(t, err, ErrAttemptTimeout)
		assert.Equal(t, int64(2), policy.Metrics().AttemptTimeouts)
		assert.Equal(t, []int{1, 2}, rec.got())
	})
}

// A downstream that returns its own timeout error, one that matches
// context.DeadlineExceeded, at the attempt deadline is cut, and the chain
// matches both: the wrap keeps the attempt's own error.
func TestPerAttemptTimeoutKeepsADownstreamDeadlineExceeded(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var rec attemptRecorder

		_, err := DoRetry[string](t.Context(), func(ctx context.Context) (string, error) {
			deadline, ok := ctx.Deadline()
			require.True(t, ok)

			// The downstream's own timer, set to the propagated deadline.
			own, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()

			<-own.Done()

			return "", own.Err()
		}, RetryParams{
			MaxAttempts: 1,
			Strategy:    ConstantBackoff(0),
			Hooks:       rec.hooks(),
			Clock:       RealClock{},
			Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
		})

		require.ErrorIs(t, err, ErrAttemptTimeout)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, []int{1}, rec.got())
	})
}

// A cut attempt that RetryIf rejects is wrapped too and still stops the
// retry.
func TestPerAttemptTimeoutWrapsACutAttemptRetryIfRejects(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var rec attemptRecorder

		calls := 0

		_, err := DoRetry[string](t.Context(), func(ctx context.Context) (string, error) {
			calls++

			return blockUntilDone(ctx)
		}, RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(time.Millisecond),
			Hooks:       rec.hooks(),
			Clock:       RealClock{},
			Opts: []RetryOption{
				PerAttemptTimeout(10 * time.Millisecond),
				RetryIf(func(error) bool { return false }),
			},
		})

		assert.Equal(t, 1, calls, "RetryIf still stops the retry")
		require.ErrorIs(t, err, ErrAttemptTimeout)
		require.NotErrorIs(t, err, ErrRetriesExhausted)
		assert.Equal(t, []int{1}, rec.got())
	})
}

// stateCtx is a context whose deadline and error are set by the test, so that
// policyDeadlineCut can be driven into the states where the attempt
// context has not reported an end yet.
type stateCtx struct {
	context.Context

	deadline time.Time
	err      error
}

func (c stateCtx) Deadline() (time.Time, bool) { return c.deadline, !c.deadline.IsZero() }

func (c stateCtx) Err() error { return c.err }

// When the attempt context has not reported its end yet, the clock decides:
// an attempt past its deadline is cut unless the parent was cancelled.
func TestPolicyDeadlineCutOnTheClock(t *testing.T) {
	t.Parallel()

	past := time.Now().Add(-time.Second)
	later := past.Add(500 * time.Millisecond) // after the attempt's, already passed

	tests := map[string]struct {
		parent stateCtx
		want   bool
	}{
		"parent alive":           {parent: stateCtx{Context: context.Background()}, want: true},
		"parent cancelled":       {parent: stateCtx{Context: context.Background(), err: context.Canceled}, want: false},
		"parent deadline passed": {parent: stateCtx{Context: context.Background(), deadline: later, err: context.DeadlineExceeded}, want: true},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			attempt := stateCtx{Context: context.Background(), deadline: past}

			assert.Equal(t, tt.want, policyDeadlineCut(tt.parent, attempt))
		})
	}
}

// An attempt that returns exactly on its deadline, before its context reports
// it, is cut.
func TestPolicyDeadlineCutOnTheDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		parent := stateCtx{Context: context.Background()}
		attempt := stateCtx{Context: context.Background(), deadline: time.Now()}

		assert.True(t, policyDeadlineCut(parent, attempt))
	})
}
