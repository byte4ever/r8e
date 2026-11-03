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
	mu       sync.Mutex
	attempts []int
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
		assert.EqualError(t, err, "retries exhausted: attempt timeout: context deadline exceeded")
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
				MaxAttempts: 3,
				Strategy:    ConstantBackoff(time.Millisecond),
				Hooks:       rec.hooks(),
				Clock:       RealClock{},
				Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
			})

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
				MaxAttempts: 3,
				Strategy:    ConstantBackoff(time.Millisecond),
				Hooks:       rec.hooks(),
				Clock:       RealClock{},
				Opts:        []RetryOption{PerAttemptTimeout(10 * time.Millisecond)},
			})

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
