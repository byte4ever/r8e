package r8e

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// RetryAfterError wrapper
// ---------------------------------------------------------------------------

func TestRetryAfterError(t *testing.T) {
	t.Parallel()

	assert.Nil(t, RetryAfterError(nil, time.Second))

	inner := errors.New("rate limited")
	wrapped := RetryAfterError(inner, 2*time.Second)

	assert.Equal(t, "rate limited", wrapped.Error())
	require.ErrorIs(t, wrapped, inner) // Unwrap chains to the inner error

	after, ok := retryAfterFromError(wrapped)
	require.True(t, ok)
	assert.Equal(t, 2*time.Second, after)
}

func TestRetryAfterFromErrorThroughClassification(t *testing.T) {
	t.Parallel()

	// A Retry-After hint survives being marked Transient.
	err := Transient(RetryAfterError(errors.New("429"), 500*time.Millisecond))

	after, ok := retryAfterFromError(err)
	require.True(t, ok)
	assert.Equal(t, 500*time.Millisecond, after)
}

func TestRetryAfterFromErrorAbsent(t *testing.T) {
	t.Parallel()

	_, ok := retryAfterFromError(errors.New("plain"))
	assert.False(t, ok)
}

// ---------------------------------------------------------------------------
// jitteredRetryAfter
// ---------------------------------------------------------------------------

func TestJitteredRetryAfter(t *testing.T) {
	t.Parallel()

	// Non-positive and sub-resolution delays are returned unchanged.
	assert.Equal(t, time.Duration(0), jitteredRetryAfter(0))
	assert.Equal(t, time.Duration(0), jitteredRetryAfter(-time.Second))
	assert.Equal(t, 5*time.Nanosecond, jitteredRetryAfter(5*time.Nanosecond))

	// A normal delay stays within ±10%.
	const base = 500 * time.Millisecond
	for range 1000 {
		got := jitteredRetryAfter(base)
		assert.GreaterOrEqual(t, got, base-base/10)
		assert.LessOrEqual(t, got, base+base/10)
	}

	// Near math.MaxInt64 the +10% jitter overflows int64 on ~half the draws;
	// the result must always be clamped non-negative, never a wrapped negative.
	for range 1000 {
		assert.GreaterOrEqual(t, jitteredRetryAfter(math.MaxInt64), time.Duration(0))
	}
}

// ---------------------------------------------------------------------------
// DoRetry honors Retry-After over the configured backoff
// ---------------------------------------------------------------------------

func TestDoRetryHonorsRetryAfterOverBackoff(t *testing.T) {
	t.Parallel()

	clk := newImmediateTestClock()

	// Backoff is 10ms, but the error carries a 500ms Retry-After: the sleeps
	// should track the hint (±10% jitter), not the backoff.
	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", RetryAfterError(errors.New("429"), 500*time.Millisecond)
		},
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(10 * time.Millisecond),
			Hooks:       &Hooks{},
			Clock:       clk,
		},
	)
	require.ErrorIs(t, err, ErrRetriesExhausted)

	durations := clk.getDurations()
	require.NotEmpty(t, durations)

	for _, d := range durations {
		assert.GreaterOrEqual(t, d, 450*time.Millisecond, "should track Retry-After, not the 10ms backoff")
		assert.LessOrEqual(t, d, 550*time.Millisecond)
	}
}

func TestDoRetryRetryAfterZeroHintIsImmediate(t *testing.T) {
	t.Parallel()

	clk := newImmediateTestClock()

	// An explicit zero hint (via the helper) overrides the 1s backoff with a 0
	// delay — immediate retry. (The httpx parser never emits a zero hint; the
	// helper honors an explicit one.)
	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", RetryAfterError(errors.New("now"), 0)
		},
		RetryParams{
			MaxAttempts: 2,
			Strategy:    ConstantBackoff(time.Second),
			Hooks:       &Hooks{},
			Clock:       clk,
		},
	)
	require.ErrorIs(t, err, ErrRetriesExhausted)

	for _, d := range clk.getDurations() {
		assert.Equal(t, time.Duration(0), d, "a zero Retry-After hint retries immediately")
	}
}

func TestDoRetryRetryAfterCappedByMaxDelay(t *testing.T) {
	t.Parallel()

	clk := newImmediateTestClock()

	// A 10s Retry-After is capped by the 1s MaxDelay.
	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", RetryAfterError(errors.New("503"), 10*time.Second)
		},
		RetryParams{
			MaxAttempts: 2,
			Strategy:    ConstantBackoff(10 * time.Millisecond),
			Hooks:       &Hooks{},
			Clock:       clk,
			Opts:        []RetryOption{MaxDelay(time.Second)},
		},
	)
	require.ErrorIs(t, err, ErrRetriesExhausted)

	for _, d := range clk.getDurations() {
		assert.Equal(t, time.Second, d, "Retry-After must be capped by MaxDelay")
	}
}

// FuzzJitteredRetryAfter asserts the jittered delay never panics and stays
// within ±10% of the input for any int64 duration — including values near
// math.MaxInt64 where the upper end would otherwise overflow and wrap negative.
func FuzzJitteredRetryAfter(f *testing.F) {
	for _, nanos := range []int64{
		0, 1, 9, 10, 100, int64(time.Second), int64(time.Hour),
		-1, math.MaxInt64, math.MinInt64, math.MaxInt64 - 1,
	} {
		f.Add(nanos)
	}

	f.Fuzz(func(t *testing.T, nanos int64) {
		delay := time.Duration(nanos)
		got := jitteredRetryAfter(delay) // must not panic

		require.GreaterOrEqual(t, got, time.Duration(0), "delay must never be negative")

		if delay <= 0 {
			require.Equal(t, time.Duration(0), got)

			return
		}

		jitter := delay / 10
		require.GreaterOrEqual(t, got, delay-jitter, "below the -10%% floor")

		// The +10%% ceiling can overflow int64; when it does, the result is
		// clamped to math.MaxInt64, which trivially satisfies the upper bound.
		if hi := delay + jitter; hi >= delay {
			require.LessOrEqual(t, got, hi, "above the +10%% ceiling")
		}
	})
}
