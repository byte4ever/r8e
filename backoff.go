package r8e

import (
	"math"
	"math/rand/v2"
	"time"
)

// BackoffStrategy determines the delay between retry attempts.
//
// Pattern: Strategy — swap backoff algorithms (constant, exponential, linear,
// jitter) without changing retry logic.
type BackoffStrategy interface {
	// Delay returns the duration to wait before the given retry attempt
	// (0-indexed: attempt 0 is the delay before the first retry).
	Delay(attempt int) time.Duration
}

// ---------------------------------------------------------------------------
// BackoffFunc — adapter for plain functions
// ---------------------------------------------------------------------------

// BackoffFunc adapts an ordinary function into a [BackoffStrategy].
// This allows callers to provide ad-hoc backoff logic without defining a type.
type BackoffFunc func(attempt int) time.Duration

// Delay calls the underlying function.
func (f BackoffFunc) Delay(attempt int) time.Duration { return f(attempt) }

// ---------------------------------------------------------------------------
// ConstantBackoff
// ---------------------------------------------------------------------------

// constantBackoff returns the same delay for every attempt.
type constantBackoff struct {
	d time.Duration
}

func (b *constantBackoff) Delay(_ int) time.Duration { return b.d }

// ConstantBackoff returns a [BackoffStrategy] that always returns a fixed
// delay d regardless of the attempt number.
func ConstantBackoff(d time.Duration) BackoffStrategy {
	return &constantBackoff{d: d}
}

// ---------------------------------------------------------------------------
// ExponentialBackoff
// ---------------------------------------------------------------------------

// exponentialBackoff returns base * 2^attempt.
type exponentialBackoff struct {
	base time.Duration
}

func (b *exponentialBackoff) Delay(attempt int) time.Duration {
	return time.Duration(float64(b.base) * math.Pow(2, float64(attempt)))
}

// ExponentialBackoff returns a [BackoffStrategy] whose delay doubles with each
// attempt: base * 2^attempt.
func ExponentialBackoff(base time.Duration) BackoffStrategy {
	return &exponentialBackoff{base: base}
}

// ---------------------------------------------------------------------------
// LinearBackoff
// ---------------------------------------------------------------------------

// linearBackoff returns step * (attempt + 1).
type linearBackoff struct {
	step time.Duration
}

func (b *linearBackoff) Delay(attempt int) time.Duration {
	return b.step * time.Duration(attempt+1)
}

// LinearBackoff returns a [BackoffStrategy] whose delay increases linearly:
// step * (attempt + 1).
func LinearBackoff(step time.Duration) BackoffStrategy {
	return &linearBackoff{step: step}
}

// ---------------------------------------------------------------------------
// ExponentialJitterBackoff
// ---------------------------------------------------------------------------

// exponentialJitterBackoff returns a random duration in [0, base * 2^attempt].
type exponentialJitterBackoff struct {
	base time.Duration
}

func (b *exponentialJitterBackoff) Delay(attempt int) time.Duration {
	max := int64(float64(b.base) * math.Pow(2, float64(attempt)))
	if max <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(max + 1))
}

// ExponentialJitterBackoff returns a [BackoffStrategy] whose delay is a random
// duration uniformly distributed in [0, base * 2^attempt]. This prevents
// thundering-herd problems by spreading retries across time.
func ExponentialJitterBackoff(base time.Duration) BackoffStrategy {
	return &exponentialJitterBackoff{base: base}
}
