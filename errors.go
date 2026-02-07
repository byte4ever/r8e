package r8e

import "errors"

// ---------------------------------------------------------------------------
// Error classification wrappers
// ---------------------------------------------------------------------------

// transientError marks a wrapped error as transient (retriable).
type transientError struct {
	err error
}

func (e *transientError) Error() string { return "transient: " + e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// permanentError marks a wrapped error as permanent (non-retriable).
type permanentError struct {
	err error
}

func (e *permanentError) Error() string { return "permanent: " + e.err.Error() }
func (e *permanentError) Unwrap() error { return e.err }

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
	if errors.As(err, &pe) {
		return false
	}
	return true
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

// ---------------------------------------------------------------------------
// ResilienceError interface and sentinel errors
// ---------------------------------------------------------------------------

// ResilienceError is implemented by all sentinel resilience errors produced
// by this package. It allows callers to distinguish infrastructure errors
// from application errors using [errors.As].
type ResilienceError interface {
	error
	IsResilience() bool
}

// resilienceError is the concrete type backing all sentinel errors.
type resilienceError string

func (e resilienceError) Error() string      { return string(e) }
func (e resilienceError) IsResilience() bool  { return true }

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
