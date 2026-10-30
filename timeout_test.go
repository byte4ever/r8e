package r8e_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/byte4ever/r8e"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Tests: Function completes before timeout -> return result
// ---------------------------------------------------------------------------

func TestDoTimeoutSuccessBeforeDeadline(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}

	result, err := r8e.DoTimeout[string](
		context.Background(),
		time.Second,
		func(_ context.Context) (string, error) {
			return "hello", nil
		},
		hooks,
	)
	require.NoError(t, err)
	require.Equal(t, "hello", result)
}

// ---------------------------------------------------------------------------
// Tests: Function completes before timeout with error -> return error
// ---------------------------------------------------------------------------

func TestDoTimeoutFnErrorBeforeDeadline(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}
	sentinel := errors.New("application error")

	result, err := r8e.DoTimeout[int](
		context.Background(),
		time.Second,
		func(_ context.Context) (int, error) {
			return 0, sentinel
		},
		hooks,
	)

	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 0, result)
}

// ---------------------------------------------------------------------------
// Tests: Function exceeds timeout -> r8e.ErrTimeout
// ---------------------------------------------------------------------------

func TestDoTimeoutExceedsDeadline(t *testing.T) {
	t.Parallel()

	// Virtual time: fn runs well past the deadline without cooperating, so the
	// timeout fires first deterministically. (A cooperative fn that returns
	// ctx.Err() exactly at the deadline races DoTimeout's select between the
	// result channel and the timeout, which is inherently ambiguous.)
	synctest.Test(t, func(t *testing.T) {
		result, err := r8e.DoTimeout[string](
			context.Background(),
			10*time.Millisecond,
			func(ctx context.Context) (string, error) {
				<-ctx.Done()

				return "late", ctx.Err()
			},
			nil,
		)

		require.ErrorIs(t, err, r8e.ErrTimeout)
		// Zero-value should be returned on timeout.
		require.Equal(t, "", result)
	})
}

// ---------------------------------------------------------------------------
// Tests: Parent context already cancelled -> context error
// ---------------------------------------------------------------------------

func TestDoTimeoutParentContextAlreadyCancelled(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	result, err := r8e.DoTimeout[int](
		ctx,
		time.Second,
		func(ctx context.Context) (int, error) {
			return 42, nil
		},
		hooks,
	)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 0, result)
}

// ---------------------------------------------------------------------------
// Tests: Parent context cancelled during execution -> parent context error
// ---------------------------------------------------------------------------

func TestDoTimeoutParentContextCancelledDuringExecution(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}

	ctx, cancel := context.WithCancel(context.Background())

	result, err := r8e.DoTimeout[string](
		ctx,
		5*time.Second, // long timeout, parent cancels first
		func(ctx context.Context) (string, error) {
			cancel() // cancel parent
			<-ctx.Done()
			return "", ctx.Err()
		},
		hooks,
	)

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, "", result)
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook fired on timeout
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookFired(t *testing.T) {
	t.Parallel()

	var hookCalled atomic.Bool
	hooks := &r8e.Hooks{
		OnTimeout: func() {
			hookCalled.Store(true)
		},
	}

	_, _ = r8e.DoTimeout[string](
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		hooks,
	)

	require.True(t, hookCalled.Load())
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook NOT fired on success
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookNotFiredOnSuccess(t *testing.T) {
	t.Parallel()

	var hookCalled atomic.Bool
	hooks := &r8e.Hooks{
		OnTimeout: func() {
			hookCalled.Store(true)
		},
	}

	_, err := r8e.DoTimeout[string](
		context.Background(),
		time.Second,
		func(_ context.Context) (string, error) {
			return "ok", nil
		},
		hooks,
	)
	require.NoError(t, err)
	require.False(t, hookCalled.Load())
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook NOT fired on fn error (non-timeout)
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookNotFiredOnFnError(t *testing.T) {
	t.Parallel()

	var hookCalled atomic.Bool
	hooks := &r8e.Hooks{
		OnTimeout: func() {
			hookCalled.Store(true)
		},
	}

	_, _ = r8e.DoTimeout[string](
		context.Background(),
		time.Second,
		func(_ context.Context) (string, error) {
			return "", errors.New("app error")
		},
		hooks,
	)

	require.False(t, hookCalled.Load())
}

// ---------------------------------------------------------------------------
// Tests: Zero-value result returned on timeout (for typed generic)
// ---------------------------------------------------------------------------

func TestDoTimeoutZeroValueOnTimeoutInt(t *testing.T) {
	t.Parallel()

	hooks := &r8e.Hooks{}

	result, err := r8e.DoTimeout[int](
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) (int, error) {
			<-ctx.Done()
			return 999, ctx.Err()
		},
		hooks,
	)

	require.ErrorIs(t, err, r8e.ErrTimeout)
	require.Equal(t, 0, result)
}

func TestDoTimeoutZeroValueOnTimeoutStruct(t *testing.T) {
	t.Parallel()

	type payload struct {
		Name  string
		Count int
	}
	hooks := &r8e.Hooks{}

	result, err := r8e.DoTimeout[payload](
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) (payload, error) {
			<-ctx.Done()
			return payload{Name: "late", Count: 42}, ctx.Err()
		},
		hooks,
	)

	require.ErrorIs(t, err, r8e.ErrTimeout)
	require.Equal(t, payload{}, result)
}

// ---------------------------------------------------------------------------
// Tests: Nil hooks do not panic
// ---------------------------------------------------------------------------

func TestDoTimeoutNilHooksDoNotPanic(t *testing.T) {
	t.Parallel()

	var hooks *r8e.Hooks // nil *Hooks must be a no-op

	_, _ = r8e.DoTimeout[string](
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		hooks,
	)
	// If we get here without panicking, the test passes.
}

// ---------------------------------------------------------------------------
// Tests: Fn returns result even after slow work within deadline
// ---------------------------------------------------------------------------

func TestDoTimeoutSlowButWithinDeadline(t *testing.T) {
	t.Parallel()

	// Deterministic virtual time: the 10ms of work resolves before the 500ms
	// deadline without any real waiting.
	synctest.Test(t, func(t *testing.T) {
		hooks := &r8e.Hooks{}

		result, err := r8e.DoTimeout[string](
			context.Background(),
			500*time.Millisecond,
			func(_ context.Context) (string, error) {
				time.Sleep(10 * time.Millisecond)

				return "slow-ok", nil
			},
			hooks,
		)
		require.NoError(t, err)
		require.Equal(t, "slow-ok", result)
	})
}

// ---------------------------------------------------------------------------
// Tests: OnTimeout hook NOT fired on parent context cancellation
// ---------------------------------------------------------------------------

func TestDoTimeoutOnTimeoutHookNotFiredOnParentCancel(t *testing.T) {
	t.Parallel()

	var hookCalled atomic.Bool
	hooks := &r8e.Hooks{
		OnTimeout: func() {
			hookCalled.Store(true)
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	_, _ = r8e.DoTimeout[string](
		ctx,
		time.Second,
		func(ctx context.Context) (string, error) {
			return "x", nil
		},
		hooks,
	)

	require.False(t, hookCalled.Load())
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkTimeout(b *testing.B) {
	hooks := &r8e.Hooks{}
	ctx := context.Background()

	for b.Loop() {
		_, _ = r8e.DoTimeout[string](
			ctx,
			time.Second,
			func(_ context.Context) (string, error) {
				return "ok", nil
			},
			hooks,
		)
	}
}

// ---------------------------------------------------------------------------
// Tests: the caller's deadline against the timeout
// ---------------------------------------------------------------------------

// blockOnCtx is a downstream that only returns once its context ends.
func blockOnCtx(ctx context.Context) (int, error) {
	<-ctx.Done()

	return 0, ctx.Err()
}

func TestDoTimeoutCallerDeadlineEqualToTimeoutIsTheCallers(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var timeouts atomic.Int32

		hooks := &r8e.Hooks{OnTimeout: func() { timeouts.Add(1) }}

		// Both deadlines fall on the same instant. The timeout does not end
		// the call strictly first, so the call is the caller's every time,
		// and the caller's context already says so when DoTimeout returns.
		for range 300 {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)

			_, err := r8e.DoTimeout(ctx, time.Second, blockOnCtx, hooks)

			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.NotErrorIs(t, err, r8e.ErrTimeout)
			require.Error(t, ctx.Err(), "the caller's context has ended by the time DoTimeout returns")
			require.True(t, r8e.IsCallerCancellation(ctx, err))
			cancel()
		}

		require.Zero(t, timeouts.Load(), "OnTimeout fired for a deadline that was the caller's")
	})
}

func TestDoTimeoutCallerDeadlineAgainstTheTimeout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantErr      error
		name         string
		callerAfter  time.Duration
		cancelAfter  time.Duration
		wantTimeouts int32
	}{
		{
			name:         "later caller deadline: the timeout is r8e's",
			callerAfter:  10 * time.Second,
			wantErr:      r8e.ErrTimeout,
			wantTimeouts: 1,
		},
		{
			name:        "earlier caller deadline: the caller's",
			callerAfter: 500 * time.Millisecond,
			wantErr:     context.DeadlineExceeded,
		},
		{
			name:        "caller cancels under an earlier deadline",
			callerAfter: 500 * time.Millisecond,
			cancelAfter: 100 * time.Millisecond,
			wantErr:     context.Canceled,
		},
		{
			name:        "caller cancels under a later deadline",
			callerAfter: 10 * time.Second,
			cancelAfter: 100 * time.Millisecond,
			wantErr:     context.Canceled,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var timeouts atomic.Int32

				ctx, cancelDeadline := context.WithTimeout(t.Context(), tt.callerAfter)
				defer cancelDeadline()

				ctx, cancel := context.WithCancel(ctx)
				defer cancel()

				if tt.cancelAfter > 0 {
					time.AfterFunc(tt.cancelAfter, cancel)
				}

				_, err := r8e.DoTimeout(ctx, time.Second, blockOnCtx,
					&r8e.Hooks{OnTimeout: func() { timeouts.Add(1) }})

				require.ErrorIs(t, err, tt.wantErr)
				require.Equal(t, tt.wantTimeouts, timeouts.Load())
			})
		})
	}
}

func TestPolicyCallerDeadlineEqualToTimeoutLeavesBreakerClosed(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var timeouts atomic.Int32

		p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
			r8e.WithTimeout(time.Second),
			r8e.WithCircuitBreaker(
				r8e.FailureThreshold(1),
				r8e.CircuitBreakerClassifier(r8e.CallerCancellationIsNeutral),
			),
			r8e.WithHooks(&r8e.Hooks{OnTimeout: func() { timeouts.Add(1) }}),
		)

		// The caller and the policy agree on when the call ends; the classifier
		// attributes it to the caller, and so does the error the caller gets.
		for range 50 {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)

			_, err := p.Do(ctx, blockOnCtx)

			cancel()
			require.ErrorIs(t, err, context.DeadlineExceeded)
			require.NotErrorIs(t, err, r8e.ErrTimeout)
		}

		synctest.Wait()
		require.Zero(t, timeouts.Load())
		require.Equal(t, string(r8e.CircuitClosed), p.Metrics().CircuitState)
	})
}

func TestNestedPolicyCallerDeadlineKeepsOuterBreakerClosed(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		inner := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
			r8e.WithTimeout(time.Second),
		)
		outer := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
			r8e.WithCircuitBreaker(
				r8e.FailureThreshold(1),
				r8e.CircuitBreakerClassifier(r8e.CallerCancellationIsNeutral),
			),
		)

		// The outer breaker classifies with the caller's context right after
		// the inner policy returns; that context must already have ended.
		for range 300 {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)

			_, err := outer.Do(ctx, func(ctx context.Context) (int, error) {
				return inner.Do(ctx, blockOnCtx)
			})

			cancel()
			require.ErrorIs(t, err, context.DeadlineExceeded)
		}

		require.Equal(t, string(r8e.CircuitClosed), outer.Metrics().CircuitState)
	})
}

// ---------------------------------------------------------------------------
// Tests: the caller's deadline against a time budget
// ---------------------------------------------------------------------------

func TestTimeBudgetCallerDeadlineAgainstTheBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantErr     error
		name        string
		opts        []r8e.TimeBudgetOption
		callerAfter time.Duration
		retries     int
		wantHooks   int32
		calls       int
		callers     bool
	}{
		{
			name:        "equal deadlines, propagated budget: the caller's",
			opts:        []r8e.TimeBudgetOption{r8e.PropagateDeadline()},
			callerAfter: time.Second,
			retries:     1,
			wantErr:     context.DeadlineExceeded,
			calls:       300,
			callers:     true,
		},
		{
			name:        "equal deadlines, propagated budget, retries left: the caller's",
			opts:        []r8e.TimeBudgetOption{r8e.PropagateDeadline()},
			callerAfter: time.Second,
			retries:     3,
			wantErr:     context.DeadlineExceeded,
			calls:       300,
			callers:     true,
		},
		{
			name:        "deadline adopted, propagated budget: the caller's",
			opts:        []r8e.TimeBudgetOption{r8e.PropagateDeadline(), r8e.RespectInboundDeadline()},
			callerAfter: 500 * time.Millisecond,
			retries:     1,
			wantErr:     context.DeadlineExceeded,
			calls:       300,
			callers:     true,
		},
		{
			name:        "deadline adopted, propagated budget, retries left: the caller's",
			opts:        []r8e.TimeBudgetOption{r8e.PropagateDeadline(), r8e.RespectInboundDeadline()},
			callerAfter: 500 * time.Millisecond,
			retries:     3,
			wantErr:     context.DeadlineExceeded,
			calls:       300,
			callers:     true,
		},
		{
			name:        "deadline adopted, cooperative budget, retries left: the caller's",
			opts:        []r8e.TimeBudgetOption{r8e.RespectInboundDeadline()},
			callerAfter: 500 * time.Millisecond,
			retries:     3,
			wantErr:     context.DeadlineExceeded,
			calls:       300,
			callers:     true,
		},
		{
			name:        "later caller deadline: the budget's",
			opts:        []r8e.TimeBudgetOption{r8e.PropagateDeadline()},
			callerAfter: 10 * time.Second,
			retries:     1,
			wantErr:     r8e.ErrTimeBudgetExceeded,
			wantHooks:   1,
			calls:       1,
		},
		{
			name:        "later caller deadline, retries left: the budget's",
			opts:        []r8e.TimeBudgetOption{r8e.PropagateDeadline()},
			callerAfter: 10 * time.Second,
			retries:     3,
			wantErr:     r8e.ErrTimeBudgetExceeded,
			wantHooks:   1,
			calls:       1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var exceeded atomic.Int32

				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(time.Second, tt.opts...),
					r8e.WithRetry(tt.retries, r8e.ConstantBackoff(time.Millisecond)),
					r8e.WithHooks(&r8e.Hooks{OnTimeBudgetExceeded: func() { exceeded.Add(1) }}),
				)

				for range tt.calls {
					ctx, cancel := context.WithTimeout(t.Context(), tt.callerAfter)

					_, err := p.Do(ctx, blockOnCtx)

					require.ErrorIs(t, err, tt.wantErr)

					if tt.callers {
						require.NotErrorIs(t, err, r8e.ErrTimeBudgetExceeded)
						require.Error(t, ctx.Err(), "the caller's context has ended by the time Do returns")
						require.True(t, r8e.IsCallerCancellation(ctx, err))
					}

					cancel()
				}

				require.Equal(t, tt.wantHooks, exceeded.Load())
			})
		})
	}
}

func TestTimeBudgetAdoptingThePolicyTimeoutLeavesItTheTimeouts(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var timeouts, exceeded atomic.Int32

		// The budget adopts the policy's own 1s timeout, which binds: the
		// call is the timeout's, not the budget's.
		p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
			r8e.WithTimeout(time.Second),
			r8e.WithTimeBudget(10*time.Second, r8e.PropagateDeadline(), r8e.RespectInboundDeadline()),
			// Retries left, so retry's budget check runs after the timeout.
			r8e.WithRetry(3, r8e.ConstantBackoff(time.Millisecond)),
			r8e.WithHooks(&r8e.Hooks{
				OnTimeout:            func() { timeouts.Add(1) },
				OnTimeBudgetExceeded: func() { exceeded.Add(1) },
			}),
		)

		for range 300 {
			_, err := p.Do(t.Context(), blockOnCtx)
			require.ErrorIs(t, err, r8e.ErrTimeout)
			require.NotErrorIs(t, err, r8e.ErrTimeBudgetExceeded)
		}

		synctest.Wait()
		require.EqualValues(t, 300, timeouts.Load())
		require.Zero(t, exceeded.Load())
	})
}

func TestNestedBudgetCallerDeadlineKeepsOuterBreakerClosed(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		inner := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
			r8e.WithTimeBudget(time.Second, r8e.PropagateDeadline(), r8e.RespectInboundDeadline()),
			r8e.WithRetry(1, r8e.ConstantBackoff(time.Millisecond)),
		)
		outer := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
			r8e.WithCircuitBreaker(
				r8e.FailureThreshold(1),
				r8e.CircuitBreakerClassifier(r8e.CallerCancellationIsNeutral),
			),
		)

		// The inner budget adopts the caller's 500ms deadline. The outer
		// breaker classifies with the caller's context right after the inner
		// policy returns; that context must already have ended.
		for range 300 {
			ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)

			_, err := outer.Do(ctx, func(ctx context.Context) (int, error) {
				return inner.Do(ctx, blockOnCtx)
			})

			cancel()
			require.ErrorIs(t, err, context.DeadlineExceeded)
		}

		require.Equal(t, string(r8e.CircuitClosed), outer.Metrics().CircuitState)
	})
}

// lateDeadlineCtx reports a deadline far after the policy timeout, yet reads
// as ended on every Err call after the first: it plays a caller whose own,
// later deadline is reached in the instant between the timeout firing and
// DoTimeout looking at it.
type lateDeadlineCtx struct {
	context.Context

	errCalls atomic.Int32
}

func (c *lateDeadlineCtx) Deadline() (time.Time, bool) {
	return time.Now().Add(time.Hour), true
}

func (c *lateDeadlineCtx) Err() error {
	if c.errCalls.Add(1) == 1 {
		return nil
	}

	return context.DeadlineExceeded
}

func TestDoTimeoutBindingTimeoutWinsOverALaterCallerDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var timeouts atomic.Int32

		ctx := &lateDeadlineCtx{Context: context.Background()}

		_, err := r8e.DoTimeout(ctx, time.Second, blockOnCtx,
			&r8e.Hooks{OnTimeout: func() { timeouts.Add(1) }})

		require.ErrorIs(t, err, r8e.ErrTimeout, "the binding timeout came strictly first")
		require.EqualValues(t, 1, timeouts.Load())
	})
}

func TestTimeBudgetCallerCancellationAtTheRetryGate(t *testing.T) {
	t.Parallel()

	// The caller cancels at 100ms while the attempt waits on its context;
	// retry then finds a 1s backoff that no longer fits the budget. The call
	// is the caller's: no budget sentinel, no hook.
	tests := []struct {
		name        string
		opts        []r8e.TimeBudgetOption
		callerAfter time.Duration
	}{
		{name: "cooperative own budget"},
		{name: "propagated own budget", opts: []r8e.TimeBudgetOption{r8e.PropagateDeadline()}},
		{
			name:        "adopted cooperative budget",
			opts:        []r8e.TimeBudgetOption{r8e.RespectInboundDeadline()},
			callerAfter: 500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var exceeded atomic.Int32

				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(time.Second, tt.opts...),
					r8e.WithRetry(3, r8e.ConstantBackoff(time.Second)),
					r8e.WithHooks(&r8e.Hooks{OnTimeBudgetExceeded: func() { exceeded.Add(1) }}),
				)

				for range 50 {
					parent, cancelParent := t.Context(), context.CancelFunc(func() {})
					if tt.callerAfter > 0 {
						parent, cancelParent = context.WithTimeout(t.Context(), tt.callerAfter)
					}

					ctx, cancel := context.WithCancel(parent)
					time.AfterFunc(100*time.Millisecond, cancel)

					_, err := p.Do(ctx, blockOnCtx)

					require.ErrorIs(t, err, context.Canceled)
					require.NotErrorIs(t, err, r8e.ErrTimeBudgetExceeded)
					require.True(t, r8e.IsCallerCancellation(ctx, err))

					cancel()
					cancelParent()
				}

				require.Zero(t, exceeded.Load())
			})
		})
	}
}

func TestTimeBudgetBindingDeadlineWinsOverALaterCallerDeadline(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		retries int
	}{
		{name: "one attempt", retries: 1},
		{name: "retries left", retries: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var exceeded atomic.Int32

				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(time.Second, r8e.PropagateDeadline()),
					r8e.WithRetry(tt.retries, r8e.ConstantBackoff(time.Millisecond)),
					r8e.WithHooks(&r8e.Hooks{OnTimeBudgetExceeded: func() { exceeded.Add(1) }}),
				)

				// The budget's deadline comes 10ms before the caller's. The
				// downstream returns 50ms after the budget cut it, so the
				// caller's deadline has passed by the time r8e looks.
				for range 300 {
					ctx, cancel := context.WithTimeout(t.Context(), time.Second+10*time.Millisecond)

					_, err := p.Do(ctx, func(ctx context.Context) (int, error) {
						<-ctx.Done()
						time.Sleep(50 * time.Millisecond)

						return 0, ctx.Err()
					})

					cancel()
					require.ErrorIs(t, err, r8e.ErrTimeBudgetExceeded, "the budget's deadline came strictly first")
				}

				require.EqualValues(t, 300, exceeded.Load())
			})
		})
	}
}

func TestTimeBudgetDecidesAtItsOwnDeadline(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantErr     error
		name        string
		cancelAfter time.Duration
		retries     int
		wantHooks   int32
		callers     bool
	}{
		{
			name: "cancel before the cut", retries: 1, cancelAfter: 500 * time.Millisecond,
			wantErr: context.Canceled, callers: true,
		},
		{
			name: "cancel before the cut, retries left", retries: 3, cancelAfter: 500 * time.Millisecond,
			wantErr: context.Canceled, callers: true,
		},
		{
			name: "cancel while the cut attempt returns", retries: 1, cancelAfter: 1200 * time.Millisecond,
			wantErr: r8e.ErrTimeBudgetExceeded, wantHooks: 300,
		},
		{
			name: "cancel while the cut attempt returns, retries left", retries: 3,
			cancelAfter: 1200 * time.Millisecond, wantErr: r8e.ErrTimeBudgetExceeded, wantHooks: 300,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var exceeded atomic.Int32

				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(time.Second, r8e.PropagateDeadline()),
					r8e.WithRetry(tt.retries, r8e.ConstantBackoff(time.Millisecond)),
					r8e.WithHooks(&r8e.Hooks{OnTimeBudgetExceeded: func() { exceeded.Add(1) }}),
				)

				// The budget's own deadline is 1s; the downstream takes 500ms to
				// return once its context ends. The budget decides at 1s: a caller
				// gone by then keeps the call, one who leaves later does not.
				for range 300 {
					ctx, cancel := context.WithCancel(t.Context())
					time.AfterFunc(tt.cancelAfter, cancel)

					_, err := p.Do(ctx, func(ctx context.Context) (int, error) {
						<-ctx.Done()
						time.Sleep(500 * time.Millisecond)

						return 0, ctx.Err()
					})

					require.ErrorIs(t, err, tt.wantErr)
					require.Equal(t, tt.callers, r8e.IsCallerCancellation(ctx, err))

					if tt.callers {
						require.NotErrorIs(t, err, r8e.ErrTimeBudgetExceeded)
					}

					cancel()
				}

				require.Equal(t, tt.wantHooks, exceeded.Load())
			})
		})
	}
}

func TestTimeBudgetCallerDeadlineReturnsTheCallersErrorOverTheDownstreams(t *testing.T) {
	t.Parallel()

	errRPC := errors.New("rpc error: code = DeadlineExceeded")

	for _, tt := range []struct {
		name    string
		retries int
	}{
		{name: "one attempt", retries: 1},
		{name: "retries left", retries: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var exceeded atomic.Int32

				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(time.Second, r8e.PropagateDeadline()),
					r8e.WithRetry(tt.retries, r8e.ConstantBackoff(time.Millisecond)),
					r8e.WithHooks(&r8e.Hooks{OnTimeBudgetExceeded: func() { exceeded.Add(1) }}),
				)

				// The caller's 500ms deadline is the one that binds. The
				// downstream reports its own error once its context ends, as a
				// gRPC client does with a status error.
				for range 300 {
					ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)

					_, err := p.Do(ctx, func(ctx context.Context) (int, error) {
						<-ctx.Done()

						return 0, errRPC
					})

					cancel()
					require.ErrorIs(t, err, context.DeadlineExceeded)
					require.NotErrorIs(t, err, r8e.ErrTimeBudgetExceeded)
					require.NotErrorIs(t, err, r8e.ErrRetriesExhausted)
					require.NotErrorIs(t, err, errRPC, "the caller's error replaces the downstream's")
					require.True(t, r8e.IsCallerCancellation(ctx, err))
				}

				require.Zero(t, exceeded.Load())
			})
		})
	}
}

func TestTimeBudgetLeavesALiveCallersFailureAlone(t *testing.T) {
	t.Parallel()

	errDown := errors.New("downstream failed")

	tests := []struct {
		name        string
		opts        []r8e.TimeBudgetOption
		callerAfter time.Duration
		retries     int
	}{
		{name: "caller deadline first", callerAfter: 500 * time.Millisecond, retries: 1},
		{name: "caller deadline first, retries left", callerAfter: 500 * time.Millisecond, retries: 3},
		{name: "equal deadlines, retries left", callerAfter: time.Second, retries: 3},
		{
			name:        "adopted deadline, retries left",
			opts:        []r8e.TimeBudgetOption{r8e.RespectInboundDeadline()},
			callerAfter: 500 * time.Millisecond,
			retries:     3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var exceeded atomic.Int32

				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(time.Second, append([]r8e.TimeBudgetOption{r8e.PropagateDeadline()}, tt.opts...)...),
					r8e.WithRetry(tt.retries, r8e.ConstantBackoff(time.Millisecond)),
					r8e.WithHooks(&r8e.Hooks{OnTimeBudgetExceeded: func() { exceeded.Add(1) }}),
				)

				ctx, cancel := context.WithTimeout(t.Context(), tt.callerAfter)
				defer cancel()

				_, err := p.Do(ctx, func(context.Context) (int, error) { return 0, errDown })

				require.NoError(t, ctx.Err(), "the caller is still live")
				require.ErrorIs(t, err, errDown)
				require.NotErrorIs(t, err, r8e.ErrTimeBudgetExceeded)
				require.Zero(t, exceeded.Load())
			})
		})
	}
}

func TestTimeBudgetKeepsASuccessAfterTheCallerLeft(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		callerAfter time.Duration
	}{
		{name: "the call cancels its own caller"},
		{name: "the caller's deadline comes first", callerAfter: 500 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(time.Second, r8e.PropagateDeadline()),
					r8e.WithRetry(1, r8e.ConstantBackoff(time.Millisecond)),
				)

				ctx, cancel := callerContext(t.Context(), tt.callerAfter)
				defer cancel()

				got, err := p.Do(ctx, func(inner context.Context) (int, error) {
					if tt.callerAfter > 0 {
						<-inner.Done()
					} else {
						cancel()
					}

					return 42, nil
				})

				require.Error(t, ctx.Err(), "the caller has left")
				require.NoError(t, err)
				require.Equal(t, 42, got)
			})
		})
	}
}

func TestCooperativeTimeBudgetReturnsTheCallersError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		wantErr     error
		name        string
		callerAfter time.Duration
		cancelAfter time.Duration
	}{
		{name: "caller deadline", callerAfter: 500 * time.Millisecond, wantErr: context.DeadlineExceeded},
		{name: "caller cancels", cancelAfter: time.Second + 10*time.Millisecond, wantErr: context.Canceled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var exceeded atomic.Int32

				// No PropagateDeadline: the budget only gates retry and hedge.
				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(time.Second, r8e.RespectInboundDeadline()),
					r8e.WithRetry(1, r8e.ConstantBackoff(time.Millisecond)),
					r8e.WithHooks(&r8e.Hooks{OnTimeBudgetExceeded: func() { exceeded.Add(1) }}),
				)

				for range 300 {
					ctx, cancel := callerContext(t.Context(), tt.callerAfter)
					if tt.cancelAfter > 0 {
						time.AfterFunc(tt.cancelAfter, cancel)
					}

					// The downstream reports its own error once its context ends.
					_, err := p.Do(ctx, func(ctx context.Context) (int, error) {
						<-ctx.Done()

						return 0, errors.New("rpc error: code = Canceled")
					})

					require.ErrorIs(t, err, tt.wantErr)
					require.NotErrorIs(t, err, r8e.ErrRetriesExhausted)
					require.True(t, r8e.IsCallerCancellation(ctx, err))

					cancel()
				}

				require.Zero(t, exceeded.Load())
			})
		})
	}
}

func TestTimeBudgetSpentBeforeTheCallIsTheBudgets(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name    string
		retries int
	}{
		{name: "one attempt", retries: 1},
		{name: "retries left", retries: 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var exceeded atomic.Int32

				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(0, r8e.PropagateDeadline()),
					r8e.WithRetry(tt.retries, r8e.ConstantBackoff(time.Millisecond)),
					r8e.WithHooks(&r8e.Hooks{OnTimeBudgetExceeded: func() { exceeded.Add(1) }}),
				)

				// A zero budget is spent when the call starts. The caller stays
				// live, so the end the downstream reports is the budget's cut.
				for i := range 300 {
					_, err := p.Do(t.Context(), func(ctx context.Context) (int, error) {
						<-ctx.Done()

						return 0, ctx.Err()
					})

					require.NoError(t, t.Context().Err(), "the caller is still live")
					require.ErrorIs(t, err, r8e.ErrTimeBudgetExceeded)
					require.False(t, r8e.IsCallerCancellation(t.Context(), err))
					require.EqualValues(t, i+1, exceeded.Load())
				}
			})
		})
	}
}

// callerContext returns the caller's context for a table row: one that
// reaches its deadline after d, or, when d is zero, one only cancel ends.
func callerContext(
	parent context.Context,
	d time.Duration,
) (context.Context, context.CancelFunc) {
	if d > 0 {
		return context.WithTimeout(parent, d)
	}

	return context.WithCancel(parent)
}

func TestTimeBudgetWithHedgeReturnsAtTheCut(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		hedgeDelay time.Duration
		wantHedges int32
	}{
		{name: "the hedge has not fired", hedgeDelay: time.Hour},
		{name: "the hedge has fired", hedgeDelay: 100 * time.Millisecond, wantHedges: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var exceeded, hedges atomic.Int32

				p := r8e.NewPolicy[string]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(time.Second, r8e.PropagateDeadline()),
					r8e.WithHedge(tt.hedgeDelay),
					r8e.WithHooks(&r8e.Hooks{
						OnTimeBudgetExceeded: func() { exceeded.Add(1) },
						OnHedgeTriggered:     func() { hedges.Add(1) },
					}),
				)

				// Every attempt ignores its context and succeeds at 1.5s, after
				// the budget's 1s deadline. Hedge does not wait for them: the
				// call ends at the cut. The release does not depend on the call
				// returning.
				release := make(chan struct{})
				time.AfterFunc(1500*time.Millisecond, func() { close(release) })

				// Let the attempts finish before the bubble ends, even when an
				// assertion below stops the test.
				defer func() {
					<-release
					synctest.Wait()
				}()

				start := time.Now()

				_, err := p.Do(t.Context(), func(context.Context) (string, error) {
					<-release

					return "ok", nil
				})

				elapsed := time.Since(start)

				require.ErrorIs(t, err, r8e.ErrTimeBudgetExceeded)
				require.Equal(t, time.Second, elapsed, "the call ends at the cut")
				require.EqualValues(t, 1, exceeded.Load())
				require.Equal(t, tt.wantHedges, hedges.Load())
			})
		})
	}
}

func TestTimeBudgetRetriesOnALaterBudgetDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var exceeded, retries atomic.Int32

		p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
			r8e.WithTimeBudget(time.Second, r8e.PropagateDeadline()),
			r8e.WithRetry(3, r8e.ConstantBackoff(100*time.Millisecond)),
			r8e.WithHooks(&r8e.Hooks{
				OnTimeBudgetExceeded: func() { exceeded.Add(1) },
				OnRetry:              func(int, error) { retries.Add(1) },
			}),
		)

		// The caller's 500ms deadline comes before the budget's 1s, which
		// therefore stays later: the backoff after the 450ms failure fits it.
		// Retry waits, and the caller's end stops the wait.
		ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
		defer cancel()

		start := time.Now()

		_, err := p.Do(ctx, func(context.Context) (int, error) {
			time.Sleep(450 * time.Millisecond)

			return 0, errors.New("downstream failed")
		})

		elapsed := time.Since(start)

		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.NotErrorIs(t, err, r8e.ErrTimeBudgetExceeded)
		require.Zero(t, exceeded.Load())
		require.EqualValues(t, 1, retries.Load())
		require.Equal(t, 500*time.Millisecond, elapsed, "the call ends with the caller")
	})
}
