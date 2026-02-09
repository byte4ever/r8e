package r8e

import (
	"math"
	"math/rand/v2"
	"time"
)

type (
	// BackoffStrategy determines the delay between retry attempts.
	//
	// Pattern: Strategy — swap backoff algorithms (constant, exponential,
	// linear,
	// jitter) without changing retry logic.
	BackoffStrategy interface {
		// Delay returns the duration to wait before the given retry attempt
		// (0-indexed: attempt 0 is the delay before the first retry).
		Delay(attempt int) time.Duration
	}

	// BackoffFunc adapts an ordinary function into a [BackoffStrategy].
	// This allows callers to provide ad-hoc backoff logic without defining a
	// type.
	BackoffFunc func(attempt int) time.Duration

	// constantBackoff returns the same delay for every attempt.
	constantBackoff struct {
		d time.Duration
	}

	// exponentialBackoff returns base * 2^attempt.
	exponentialBackoff struct {
		base time.Duration
	}

	// linearBackoff returns step * (attempt + 1).
	linearBackoff struct {
		step time.Duration
	}

	// exponentialJitterBackoff returns a random duration in [0, base *
	// 2^attempt].
	exponentialJitterBackoff struct {
		base time.Duration
	}
)

// ---------------------------------------------------------------------------
// BackoffFunc — adapter for plain functions
// ---------------------------------------------------------------------------.

// Delay calls the underlying function.
func (f BackoffFunc) Delay(attempt int) time.Duration { return f(attempt) }

// ---------------------------------------------------------------------------
// ConstantBackoff
// ---------------------------------------------------------------------------.

func (b *constantBackoff) Delay(_ int) time.Duration { return b.d }

// ConstantBackoff returns a [BackoffStrategy] that always returns a fixed
// delay d regardless of the attempt number.
//
//nolint:ireturn,iface // each backoff function returns a distinct
// implementation of BackoffStrategy.
func ConstantBackoff(d time.Duration) BackoffStrategy {
	return &constantBackoff{d: d}
}

// ---------------------------------------------------------------------------
// ExponentialBackoff
// ---------------------------------------------------------------------------.

func (b *exponentialBackoff) Delay(attempt int) time.Duration {
	return time.Duration(float64(b.base) * math.Pow(2, float64(attempt)))
}

// ExponentialBackoff returns a [BackoffStrategy] whose delay doubles with each
// attempt: base * 2^attempt.
//
//nolint:ireturn,iface // each backoff function returns a distinct
// implementation of BackoffStrategy.
func ExponentialBackoff(base time.Duration) BackoffStrategy {
	return &exponentialBackoff{base: base}
}

// ---------------------------------------------------------------------------
// LinearBackoff
// ---------------------------------------------------------------------------.

func (b *linearBackoff) Delay(attempt int) time.Duration {
	return b.step * time.Duration(attempt+1)
}

// LinearBackoff returns a [BackoffStrategy] whose delay increases linearly:
// step * (attempt + 1).
//
//nolint:ireturn,iface // each backoff function returns a distinct
// implementation of BackoffStrategy.
func LinearBackoff(step time.Duration) BackoffStrategy {
	return &linearBackoff{step: step}
}

// ---------------------------------------------------------------------------
// ExponentialJitterBackoff
// ---------------------------------------------------------------------------.

func (b *exponentialJitterBackoff) Delay(attempt int) time.Duration {
	ceiling := int64(float64(b.base) * math.Pow(2, float64(attempt)))
	if ceiling <= 0 {
		return 0
	}

	return time.Duration(rand.Int64N(ceiling + 1))
}

// ExponentialJitterBackoff returns a [BackoffStrategy] whose delay is a random
// duration uniformly distributed in [0, base * 2^attempt]. This prevents
// thundering-herd problems by spreading retries across time.
//
//nolint:ireturn,iface // each backoff function returns a distinct
// implementation of BackoffStrategy.
func ExponentialJitterBackoff(base time.Duration) BackoffStrategy {
	return &exponentialJitterBackoff{base: base}
}
