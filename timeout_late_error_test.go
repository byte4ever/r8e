package r8e_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/byte4ever/r8e"
)

var errOwnDeadline = errors.New("downstream deadline")

// stopOnDeadline is a downstream that stops on the propagated deadline by
// itself, as a socket or a client deadline does: it returns its own error once
// the deadline has passed, without waiting for the context to report it.
func stopOnDeadline(ctx context.Context) (int, error) {
	deadline, ok := ctx.Deadline()
	if !ok {
		return 0, errors.New("no deadline propagated")
	}

	time.Sleep(time.Until(deadline))

	return 0, errOwnDeadline
}

// On the real clock, a downstream that returns its own error at the instant
// the timeout fires gets one outcome on every call: ErrTimeout and one
// OnTimeout, from DoTimeout and from WithTimeout in a policy alike, as a time
// budget with PropagateDeadline answers ErrTimeBudgetExceeded.
func TestTimeoutAttributesADownstreamStoppingOnItsDeadline(t *testing.T) {
	t.Parallel()

	const calls = 500

	tests := map[string]struct {
		wantErr error
		call    func(*r8e.Hooks) func(context.Context) error
		hook    func(*r8e.Hooks, *atomic.Int32)
	}{
		"DoTimeout": {
			wantErr: r8e.ErrTimeout,
			call: func(hooks *r8e.Hooks) func(context.Context) error {
				return func(ctx context.Context) error {
					_, err := r8e.DoTimeout(ctx, 5*time.Millisecond, stopOnDeadline, hooks)

					return err
				}
			},
			hook: func(h *r8e.Hooks, n *atomic.Int32) { h.OnTimeout = func() { n.Add(1) } },
		},
		"WithTimeout": {
			wantErr: r8e.ErrTimeout,
			call: func(hooks *r8e.Hooks) func(context.Context) error {
				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeout(5*time.Millisecond), r8e.WithHooks(hooks))

				return func(ctx context.Context) error {
					_, err := p.Do(ctx, stopOnDeadline)

					return err
				}
			},
			hook: func(h *r8e.Hooks, n *atomic.Int32) { h.OnTimeout = func() { n.Add(1) } },
		},
		"WithTimeBudget with PropagateDeadline": {
			wantErr: r8e.ErrTimeBudgetExceeded,
			call: func(hooks *r8e.Hooks) func(context.Context) error {
				p := r8e.NewPolicy[int]("", r8e.WithRegistry(r8e.NewRegistry()),
					r8e.WithTimeBudget(5*time.Millisecond, r8e.PropagateDeadline()),
					r8e.WithRetry(1, r8e.ConstantBackoff(time.Millisecond)), r8e.WithHooks(hooks))

				return func(ctx context.Context) error {
					_, err := p.Do(ctx, stopOnDeadline)

					return err
				}
			},
			hook: func(h *r8e.Hooks, n *atomic.Int32) { h.OnTimeBudgetExceeded = func() { n.Add(1) } },
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var fired atomic.Int32

			hooks := &r8e.Hooks{}
			tt.hook(hooks, &fired)
			call := tt.call(hooks)

			var (
				wg     sync.WaitGroup
				mu     sync.Mutex
				wrong  int
				sample string
			)

			for range calls {
				wg.Go(func() {
					if err := call(t.Context()); !errors.Is(err, tt.wantErr) {
						mu.Lock()
						defer mu.Unlock()

						wrong++
						sample = fmt.Sprintf("%v", err)
					}
				})
			}

			wg.Wait()

			assert.Zero(t, wrong, "calls with another outcome, e.g. %s", sample)
			assert.EqualValues(t, calls, fired.Load(), "one hook per call")
		})
	}
}

// DoTimeout decides when the binding timeout fires. An error fn returns after
// it is ErrTimeout; a caller that cancels before it keeps the call; an error
// or a success before it is fn's own.
func TestDoTimeoutDecidesWhenTheTimeoutFires(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")

	tests := map[string]struct {
		wantErr      error
		cancelAfter  time.Duration
		fn           func(context.Context) (int, error)
		wantVal      int
		wantTimeouts int32
	}{
		"own error on the deadline": {
			fn: func(ctx context.Context) (int, error) {
				_, err := stopOnDeadline(ctx)

				return 42, err
			},
			wantErr: r8e.ErrTimeout, wantTimeouts: 1,
		},
		"own error after the deadline, context ignored": {
			fn: func(context.Context) (int, error) {
				time.Sleep(20 * time.Millisecond)

				return 0, errBoom
			},
			wantErr: r8e.ErrTimeout, wantTimeouts: 1,
		},
		"own error before the deadline": {
			fn:      func(context.Context) (int, error) { return 0, errBoom },
			wantErr: errBoom,
		},
		"caller cancels before the deadline": {
			cancelAfter: 5 * time.Millisecond,
			fn: func(ctx context.Context) (int, error) {
				<-ctx.Done()
				time.Sleep(10 * time.Millisecond)

				return 0, errBoom
			},
			wantErr: context.Canceled,
		},
		"success before the deadline": {
			fn:      func(context.Context) (int, error) { return 7, nil },
			wantVal: 7,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var timeouts atomic.Int32

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				if tt.cancelAfter > 0 {
					time.AfterFunc(tt.cancelAfter, cancel)
				}

				val, err := r8e.DoTimeout(ctx, 10*time.Millisecond, tt.fn,
					&r8e.Hooks{OnTimeout: func() { timeouts.Add(1) }})

				if tt.wantErr == nil {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, tt.wantErr)
				}

				assert.Equal(t, tt.wantVal, val)
				assert.Equal(t, tt.wantTimeouts, timeouts.Load())

				// DoTimeout does not wait for fn: let it return before the
				// bubble ends.
				time.Sleep(time.Second)
			})
		})
	}
}

// cancelledAfterFirstCtx reads as live on the first Err call and as cancelled
// on every later one, with no deadline and a Done that never closes: it plays
// a caller that cancels in the instant between the binding timeout firing and
// DoTimeout looking at it.
type cancelledAfterFirstCtx struct {
	context.Context

	errCalls atomic.Int32
}

func (c *cancelledAfterFirstCtx) Err() error {
	if c.errCalls.Add(1) == 1 {
		return nil
	}

	return context.Canceled
}

// A caller that cancels after the binding timeout fired does not take the call
// back: it is ErrTimeout, with one OnTimeout.
func TestDoTimeoutLateCancelDoesNotTakeTheCallBack(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var timeouts atomic.Int32

		ctx := &cancelledAfterFirstCtx{Context: context.Background()}

		_, err := r8e.DoTimeout(ctx, time.Second, blockOnCtx,
			&r8e.Hooks{OnTimeout: func() { timeouts.Add(1) }})

		require.ErrorIs(t, err, r8e.ErrTimeout)
		require.EqualValues(t, 1, timeouts.Load())
	})
}

// A caller that ends the call gets its own error back, even when fn returns
// an error of its own at that instant: the caller's cancellation, or the
// caller's deadline when the timeout does not bind.
func TestDoTimeoutCallerEndReturnsTheCallersError(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")

	ownErr := func(ctx context.Context) (int, error) {
		<-ctx.Done()

		return 0, errBoom
	}

	tests := map[string]struct {
		caller  func(context.Context) (context.Context, context.CancelFunc)
		wantErr error
	}{
		"caller cancels": {
			caller: func(ctx context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(ctx)
				time.AfterFunc(5*time.Millisecond, cancel)

				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
		"caller deadline first": {
			caller: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return context.WithTimeout(ctx, 5*time.Millisecond)
			},
			wantErr: context.DeadlineExceeded,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var timeouts atomic.Int32

				for range 200 {
					ctx, cancel := tt.caller(t.Context())

					_, err := r8e.DoTimeout(ctx, 10*time.Millisecond, ownErr,
						&r8e.Hooks{OnTimeout: func() { timeouts.Add(1) }})

					require.ErrorIs(t, err, tt.wantErr)
					require.True(t, r8e.IsCallerCancellation(ctx, err))

					cancel()
				}

				assert.Zero(t, timeouts.Load())
			})
		})
	}
}

// A success that reaches DoTimeout before it sees the timeout stays a success,
// even on the deadline: across many calls, each one is either the success or
// a cut, and the success shows up.
func TestDoTimeoutSuccessOnTheDeadlineStaysASuccess(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		succeedOnDeadline := func(ctx context.Context) (int, error) {
			deadline, _ := ctx.Deadline()
			time.Sleep(time.Until(deadline))

			return 7, nil
		}

		var successes, cuts int

		for range 200 {
			var timeouts atomic.Int32

			val, err := r8e.DoTimeout(t.Context(), 10*time.Millisecond, succeedOnDeadline,
				&r8e.Hooks{OnTimeout: func() { timeouts.Add(1) }})

			switch {
			case err == nil:
				successes++

				assert.Equal(t, 7, val)
				assert.Zero(t, timeouts.Load())
			default:
				cuts++

				require.ErrorIs(t, err, r8e.ErrTimeout)
				assert.EqualValues(t, 1, timeouts.Load())
			}

			synctest.Wait()
		}

		assert.Positive(t, successes, "a success seen first stays a success")
		assert.Equal(t, 200, successes+cuts)
	})
}
