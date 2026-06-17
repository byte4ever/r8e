package r8e_test

import (
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
	require.Equal(t, "transient: connection reset", err.Error())
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
	require.Equal(t, "permanent: invalid argument", err.Error())
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
