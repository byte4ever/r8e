package r8e

import (
	"errors"
	"math"
	"math/rand/v2"
	"time"
)

// ---------------------------------------------------------------------------
// Retry-After — honor a server-supplied retry delay over the computed backoff
// ---------------------------------------------------------------------------.

type (
	// RetryAfterProvider is implemented by errors that carry a server-supplied
	// delay (e.g. an HTTP 429/503 Retry-After header). [DoRetry] honors it over
	// the configured backoff. Implement it on your own error type to have retry
	// honor a delay, or attach a fixed one to any error with [RetryAfterError];
	// the httpx adapter's StatusError implements it from the HTTP header.
	RetryAfterProvider interface {
		// RetryAfter returns how long to wait before the next attempt, and whether
		// a hint is present.
		RetryAfter() (time.Duration, bool)
	}

	// retryAfterError wraps an error with a fixed retry-after delay.
	retryAfterError struct {
		err   error
		after time.Duration
	}
)

// retryAfterJitterFraction is the ±fraction of randomised jitter applied to a
// Retry-After delay, spreading synchronised retries to avoid a thundering herd.
const retryAfterJitterFraction = 10 // one tenth = ±10%

// RetryAfterError wraps err with a retry-after delay that [DoRetry] honors as the
// backoff before the next attempt (in place of the configured strategy), capped
// by any MaxDelay. The wrapped error keeps its own classification — it is
// retryable unless err is [Permanent]. Returns nil if err is nil.
func RetryAfterError(err error, after time.Duration) error {
	if err == nil {
		return nil
	}

	return &retryAfterError{err: err, after: after}
}

func (e *retryAfterError) Error() string { return e.err.Error() }
func (e *retryAfterError) Unwrap() error { return e.err }

// RetryAfter reports the wrapped retry-after delay.
func (e *retryAfterError) RetryAfter() (time.Duration, bool) {
	return e.after, true
}

// retryAfterFromError returns the retry-after hint carried by err (or any error
// it wraps), if any.
func retryAfterFromError(err error) (time.Duration, bool) {
	var provider RetryAfterProvider
	if errors.As(err, &provider) {
		return provider.RetryAfter()
	}

	return 0, false
}

// jitteredRetryAfter returns d spread uniformly within ±10% to avoid a
// thundering herd when many clients receive the same Retry-After. A non-positive
// or sub-10ns delay is returned unchanged (no room to jitter).
func jitteredRetryAfter(delay time.Duration) time.Duration {
	if delay <= 0 {
		return 0
	}

	jitter := delay / retryAfterJitterFraction
	if jitter <= 0 {
		return delay
	}

	// Uniform in [delay-jitter, delay+jitter]. For a delay near math.MaxInt64
	// the upper end can exceed int64 and wrap negative; clamp it to the maximum
	// duration in that case.
	result := delay - jitter + time.Duration(rand.Int64N(int64(2*jitter)+1))
	if result < 0 {
		return math.MaxInt64
	}

	return result
}
