package r8e

import "errors"

// ---------------------------------------------------------------------------
// Error classification wrappers
// ---------------------------------------------------------------------------.

type (
	// ResilienceError identifies errors produced by the resilience layer
	// itself,
	// as opposed to errors from the wrapped function.
	//nolint:iface // exported for use in tests and consumer error
	// classification.
	ResilienceError interface {
		error
		// IsResilience reports whether this error originates from the
		// resilience layer.
		IsResilience() bool
	}

	// transientError marks a wrapped error as transient (retriable).
	transientError struct {
		err error
	}

	// permanentError marks a wrapped error as permanent (non-retriable).
	permanentError struct {
		err error
	}

	// resilienceError is the concrete type backing all sentinel errors.
	resilienceError string
)

// Sentinel resilience errors.
var (
	// ErrCircuitOpen is returned when the circuit breaker is in the open state.
	ErrCircuitOpen error = resilienceError("circuit breaker is open")
	// ErrRateLimited is returned when a request is rejected by a rate limiter.
	ErrRateLimited error = resilienceError("rate limited")
	// ErrBulkheadFull is returned when the bulkhead has no available capacity.
	ErrBulkheadFull error = resilienceError("bulkhead full")
	// ErrTimeout is returned when an operation exceeds its deadline.
	ErrTimeout error = resilienceError("timeout")
	// ErrRetriesExhausted is returned when all retry attempts have been used.
	ErrRetriesExhausted error = resilienceError("retries exhausted")
)

func (e *transientError) Error() string { return "transient: " + e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

func (e *permanentError) Error() string { return "permanent: " + e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

func (e resilienceError) Error() string { return string(e) }

// IsResilience reports whether the error is a resilience infrastructure error.
func (resilienceError) IsResilience() bool { return true }

// Transient wraps err to mark it as a transient (retriable) error.
// Returns nil if err is nil.
func Transient(err error) error {
	if err == nil {
		return nil
	}

	return &transientError{err: err}
}

// Permanent wraps err to mark it as a permanent (non-retriable) error.
// Returns nil if err is nil.
func Permanent(err error) error {
	if err == nil {
		return nil
	}

	return &permanentError{err: err}
}

// IsTransient reports whether err is transient. Unclassified (unwrapped)
// errors are treated as transient. Returns false for nil.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	// Explicitly permanent errors are not transient.
	var pe *permanentError

	return !errors.As(err, &pe)
}

// IsPermanent reports whether err was explicitly marked as permanent.
// Returns false for nil and for unclassified errors.
func IsPermanent(err error) bool {
	if err == nil {
		return false
	}

	var pe *permanentError

	return errors.As(err, &pe)
}
