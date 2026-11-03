package r8e_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/byte4ever/r8e"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Transient wrapping and detection
// ---------------------------------------------------------------------------

func TestTransientWrapsError(t *testing.T) {
	t.Parallel()

	cause := errors.New("connection reset")
	err := r8e.Transient(cause)

	require.Error(t, err)
	require.Equal(t, "connection reset", err.Error())
}

func TestTransientNilReturnsNil(t *testing.T) {
	t.Parallel()

	require.NoError(t, r8e.Transient(nil))
}

func TestIsTransientDetectsTransient(t *testing.T) {
	t.Parallel()

	err := r8e.Transient(errors.New("oops"))
	require.True(t, r8e.IsTransient(err))
}

func TestIsTransientUnclassifiedTreatedAsTransient(t *testing.T) {
	t.Parallel()

	err := errors.New("some random error")
	require.True(t, r8e.IsTransient(err))
}

func TestIsTransientNilReturnsFalse(t *testing.T) {
	t.Parallel()

	require.False(t, r8e.IsTransient(nil))
}

func TestIsTransientPermanentReturnsFalse(t *testing.T) {
	t.Parallel()

	err := r8e.Permanent(errors.New("bad request"))
	require.False(t, r8e.IsTransient(err))
}

// ---------------------------------------------------------------------------
// Permanent wrapping and detection
// ---------------------------------------------------------------------------

func TestPermanentWrapsError(t *testing.T) {
	t.Parallel()

	cause := errors.New("invalid argument")
	err := r8e.Permanent(cause)

	require.Error(t, err)
	require.Equal(t, "invalid argument", err.Error())
}

func TestPermanentNilReturnsNil(t *testing.T) {
	t.Parallel()

	require.NoError(t, r8e.Permanent(nil))
}

func TestIsPermanentDetectsPermanent(t *testing.T) {
	t.Parallel()

	err := r8e.Permanent(errors.New("oops"))
	require.True(t, r8e.IsPermanent(err))
}

func TestIsPermanentUnclassifiedReturnsFalse(t *testing.T) {
	t.Parallel()

	err := errors.New("some random error")
	require.False(t, r8e.IsPermanent(err))
}

func TestIsPermanentNilReturnsFalse(t *testing.T) {
	t.Parallel()

	require.False(t, r8e.IsPermanent(nil))
}

func TestIsPermanentTransientReturnsFalse(t *testing.T) {
	t.Parallel()

	err := r8e.Transient(errors.New("timeout"))
	require.False(t, r8e.IsPermanent(err))
}

// ---------------------------------------------------------------------------
// Unwrap / errors.Is / errors.As support
// ---------------------------------------------------------------------------

func TestTransientUnwrap(t *testing.T) {
	t.Parallel()

	cause := errors.New("root cause")
	err := r8e.Transient(cause)

	require.ErrorIs(t, err, cause)
}

func TestPermanentUnwrap(t *testing.T) {
	t.Parallel()

	cause := errors.New("root cause")
	err := r8e.Permanent(cause)

	require.ErrorIs(t, err, cause)
}

// Use a proper custom error for errors.As testing.
type codedError struct {
	code int
	msg  string
}

func (e *codedError) Error() string { return e.msg }

func TestTransientErrorsAsCustomType(t *testing.T) {
	t.Parallel()

	cause := &codedError{code: 42, msg: "bad thing"}
	err := r8e.Transient(cause)

	var target *codedError
	require.ErrorAs(t, err, &target)
	require.Equal(t, 42, target.code)
}

func TestPermanentErrorsAsCustomType(t *testing.T) {
	t.Parallel()

	cause := &codedError{code: 99, msg: "really bad"}
	err := r8e.Permanent(cause)

	var target *codedError
	require.ErrorAs(t, err, &target)
	require.Equal(t, 99, target.code)
}

// ---------------------------------------------------------------------------
// Wrapping wrapped transient/permanent with fmt.Errorf should still be
// detectable.
// ---------------------------------------------------------------------------

func TestIsTransientDetectsWrappedTransient(t *testing.T) {
	t.Parallel()

	inner := r8e.Transient(errors.New("timeout"))
	wrapped := fmt.Errorf("layer: %w", inner)

	require.True(t, r8e.IsTransient(wrapped))
}

func TestIsPermanentDetectsWrappedPermanent(t *testing.T) {
	t.Parallel()

	inner := r8e.Permanent(errors.New("bad input"))
	wrapped := fmt.Errorf("layer: %w", inner)

	require.True(t, r8e.IsPermanent(wrapped))
}

// Transient cannot make a permanent error retriable: the inner Permanent mark
// still stops the retry after one attempt.
func TestTransientCannotOverridePermanent(t *testing.T) {
	t.Parallel()

	cause := errors.New("http status 400")
	err := r8e.Transient(r8e.Permanent(cause))

	require.True(t, r8e.IsPermanent(err))
	require.False(t, r8e.IsTransient(err))

	calls := 0
	policy := r8e.NewPolicy[string]("",
		r8e.WithRetry(3, r8e.ConstantBackoff(0)),
	)

	_, got := policy.Do(t.Context(), func(context.Context) (string, error) {
		calls++

		return "", err
	})

	assert.Equal(t, 1, calls)
	require.ErrorIs(t, got, cause)
	assert.NotErrorIs(t, got, r8e.ErrRetriesExhausted)
}

// doWithRetries runs one Do of a two-attempt policy whose function always
// returns err, and returns the error Do returns.
func doWithRetries(t *testing.T, err error) error {
	t.Helper()

	policy := r8e.NewPolicy[string]("",
		r8e.WithRetry(2, r8e.ConstantBackoff(0)),
	)

	_, got := policy.Do(t.Context(),
		func(context.Context) (string, error) { return "", err },
	)

	return got
}

// The classification wrappers add nothing to the message Do returns: a
// transient error reads exactly as the same unclassified error, and a
// permanent one reads as its cause, still classified.
func TestPolicyErrorMessagesCarryNoClassificationPrefix(t *testing.T) {
	t.Parallel()

	t.Run("transient reads as unclassified", func(t *testing.T) {
		t.Parallel()

		cause := errors.New("http status 503")

		bare := doWithRetries(t, cause)
		marked := doWithRetries(t, r8e.Transient(cause))

		require.Error(t, bare)
		require.Error(t, marked)
		assert.Equal(t, bare.Error(), marked.Error())
		require.ErrorIs(t, marked, cause)
		require.ErrorIs(t, marked, r8e.ErrRetriesExhausted)
	})

	t.Run("permanent reads as its cause", func(t *testing.T) {
		t.Parallel()

		cause := errors.New("http status 400")

		err := doWithRetries(t, r8e.Permanent(cause))

		require.EqualError(t, err, cause.Error())
		require.ErrorIs(t, err, cause)
		assert.True(t, r8e.IsPermanent(err))
	})
}

// ---------------------------------------------------------------------------
// Sentinel errors
// ---------------------------------------------------------------------------

func TestSentinelErrorMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		want string
	}{
		{r8e.ErrCircuitOpen, "circuit breaker is open"},
		{r8e.ErrRateLimited, "rate limited"},
		{r8e.ErrBulkheadFull, "bulkhead full"},
		{r8e.ErrTimeout, "timeout"},
		{r8e.ErrRetriesExhausted, "retries exhausted"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, tt.err.Error())
		})
	}
}

func TestSentinelErrorsDetectableViaErrorsIsWhenWrapped(t *testing.T) {
	t.Parallel()

	sentinels := []error{
		r8e.ErrCircuitOpen,
		r8e.ErrRateLimited,
		r8e.ErrBulkheadFull,
		r8e.ErrTimeout,
		r8e.ErrRetriesExhausted,
	}
	for _, sentinel := range sentinels {
		t.Run(sentinel.Error(), func(t *testing.T) {
			t.Parallel()

			wrapped := fmt.Errorf("context: %w", sentinel)
			assert.ErrorIs(t, wrapped, sentinel)
		})
	}
}

// ---------------------------------------------------------------------------
// Sentinel codes
// ---------------------------------------------------------------------------

func TestErrorCode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{"circuit open", r8e.ErrCircuitOpen, "circuit_open"},
		{"circuit ramping", r8e.ErrCircuitRamping, "circuit_ramping"},
		{"rate limited", r8e.ErrRateLimited, "rate_limited"},
		{"bulkhead full", r8e.ErrBulkheadFull, "bulkhead_full"},
		{"bulkhead timeout", r8e.ErrBulkheadTimeout, "bulkhead_timeout"},
		{"codel shed", r8e.ErrCoDelShed, "codel_shed"},
		{"concurrency limited", r8e.ErrConcurrencyLimited, "concurrency_limited"},
		{"throttled", r8e.ErrThrottled, "throttled"},
		{"slo shed", r8e.ErrSLOShed, "slo_shed"},
		{"timeout", r8e.ErrTimeout, "timeout"},
		{"time budget exceeded", r8e.ErrTimeBudgetExceeded, "time_budget_exceeded"},
		{
			"concurrency budget exceeded",
			r8e.ErrConcurrencyBudgetExceeded, "concurrency_budget_exceeded",
		},
		{"retries exhausted", r8e.ErrRetriesExhausted, "retries_exhausted"},
		{"panic", &r8e.PanicError{Value: "boom"}, "panic_recovered"},
		{"chaos injected", r8e.ErrChaosInjected, "chaos_injected"},
		{"config sentinel", r8e.ErrCoalesceWithoutTimeout, "coalesce_without_timeout"},
		{"pattern absent", r8e.ErrPatternAbsent, "pattern_absent"},
		{"policy not registered", r8e.ErrPolicyNotRegistered, "policy_not_registered"},
		{"wrapped", fmt.Errorf("call: %w", r8e.ErrRateLimited), "rate_limited"},
		{"classified", r8e.Transient(r8e.ErrCircuitOpen), "circuit_open"},
		{
			"wrapped config sentinel",
			fmt.Errorf("retry: %w", r8e.ErrRetryMaxAttemptsRequired),
			"retry_max_attempts_required",
		},
		{"wrapped chaos", fmt.Errorf("call: %w", r8e.ErrChaosInjected), "chaos_injected"},
		{
			"wrapped panic",
			fmt.Errorf("call: %w", &r8e.PanicError{Value: "boom"}),
			"panic_recovered",
		},
		{
			"plain error inside retries exhausted",
			fmt.Errorf("%w: %w", r8e.ErrRetriesExhausted, errors.New("boom")),
			"retries_exhausted",
		},
		{
			"retries exhausted inside time budget, as the budget wraps a retry",
			fmt.Errorf("%w: %w", r8e.ErrTimeBudgetExceeded,
				fmt.Errorf("%w: %w", r8e.ErrRetriesExhausted, context.DeadlineExceeded)),
			"time_budget_exceeded",
		},
		{
			"first cause in pre-order",
			errors.Join(r8e.ErrRateLimited, r8e.ErrTimeout),
			"rate_limited",
		},
		{
			"cause after an envelope in a join",
			errors.Join(r8e.ErrRetriesExhausted, r8e.ErrBulkheadFull),
			"bulkhead_full",
		},
		{
			"pre-order, not level order",
			errors.Join(fmt.Errorf("svc a: %w", r8e.ErrCircuitOpen), r8e.ErrRateLimited),
			"circuit_open",
		},
		{
			"outermost envelope in pre-order",
			errors.Join(fmt.Errorf("x: %w", r8e.ErrRetriesExhausted), r8e.ErrTimeBudgetExceeded),
			"retries_exhausted",
		},
		{
			"cause under a time budget and a retry",
			fmt.Errorf("%w: %w", r8e.ErrTimeBudgetExceeded,
				fmt.Errorf("%w: %w", r8e.ErrRetriesExhausted, r8e.ErrCircuitOpen)),
			"circuit_open",
		},
		{
			"cause under nested retries",
			fmt.Errorf("%w: %w", r8e.ErrRetriesExhausted,
				fmt.Errorf("%w: %w", r8e.ErrRetriesExhausted, r8e.ErrTimeout)),
			"timeout",
		},
		{"plain error", errors.New("boom"), ""},
		{"nil", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, r8e.ErrorCode(tt.err))
		})
	}
}

// isOnlyTimeout matches ErrTimeout through an Is method only, as a caller's own
// error type might.
type isOnlyTimeout struct{}

func (isOnlyTimeout) Error() string { return "upstream timeout" }

func (isOnlyTimeout) Is(target error) bool { return target == r8e.ErrTimeout }

// ErrorCode reaches a sentinel through Unwrap only: an error that matches one
// through its own Is method carries no code, wrapped or not.
func TestErrorCodeIgnoresIsOnlyMatches(t *testing.T) {
	t.Parallel()

	require.ErrorIs(t, isOnlyTimeout{}, r8e.ErrTimeout)

	assert.Empty(t, r8e.ErrorCode(isOnlyTimeout{}))
	assert.Empty(t, r8e.ErrorCode(fmt.Errorf("call: %w", isOnlyTimeout{})))
	assert.Equal(t, "retries_exhausted",
		r8e.ErrorCode(fmt.Errorf("%w: %w", r8e.ErrRetriesExhausted, isOnlyTimeout{})))
}

// Every envelope yields to every cause it carries, so a sentinel added to the
// core reports its own code under retries or a budget with no list to update.
func TestErrorCodeEnvelopesYieldToTheirCause(t *testing.T) {
	t.Parallel()

	envelopes := []error{
		r8e.ErrRetriesExhausted,
		r8e.ErrTimeBudgetExceeded,
		r8e.ErrConcurrencyBudgetExceeded,
	}
	causes := map[string]error{
		"circuit_open":        r8e.ErrCircuitOpen,
		"circuit_ramping":     r8e.ErrCircuitRamping,
		"rate_limited":        r8e.ErrRateLimited,
		"bulkhead_full":       r8e.ErrBulkheadFull,
		"bulkhead_timeout":    r8e.ErrBulkheadTimeout,
		"codel_shed":          r8e.ErrCoDelShed,
		"concurrency_limited": r8e.ErrConcurrencyLimited,
		"throttled":           r8e.ErrThrottled,
		"slo_shed":            r8e.ErrSLOShed,
		"timeout":             r8e.ErrTimeout,
		"panic_recovered":     &r8e.PanicError{Value: "boom"},
		"chaos_injected":      r8e.ErrChaosInjected,
	}

	for _, envelope := range envelopes {
		for want, cause := range causes {
			t.Run(r8e.ErrorCode(envelope)+"/"+want, func(t *testing.T) {
				t.Parallel()

				assert.Equal(t, want, r8e.ErrorCode(fmt.Errorf("%w: %w", envelope, cause)))
			})
		}
	}
}

// Through a real policy: a panic retried to exhaustion reports the panic, not
// the retry envelope around it.
func TestErrorCodeOfRetriedPanic(t *testing.T) {
	t.Parallel()

	policy := r8e.NewPolicy[string]("",
		r8e.WithRetry(2, r8e.ConstantBackoff(0)),
		r8e.WithRecover(),
	)

	_, err := policy.Do(t.Context(), func(context.Context) (string, error) {
		panic("boom")
	})

	require.ErrorIs(t, err, r8e.ErrRetriesExhausted)
	assert.Equal(t, "panic_recovered", r8e.ErrorCode(err))
}
