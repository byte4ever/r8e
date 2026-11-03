package r8e_test

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
				wrong  atomic.Int32
				sample atomic.Value
			)

			for range calls {
				wg.Go(func() {
					if err := call(t.Context()); !errors.Is(err, tt.wantErr) {
						wrong.Add(1)
						sample.Store(err)
					}
				})
			}

			wg.Wait()

			assert.Zero(t, wrong.Load(), "calls with another outcome, e.g. %v", sample.Load())
			assert.EqualValues(t, calls, fired.Load())
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
			fn:      stopOnDeadline,
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
