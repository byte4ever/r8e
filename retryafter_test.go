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

	// A non-positive delay returns 0; a sub-10ns delay is returned unchanged.
	assert.Equal(t, time.Duration(0), jitteredRetryAfter(0))
	assert.Equal(t, time.Duration(0), jitteredRetryAfter(-time.Second))
	assert.Equal(t, 5*time.Nanosecond, jitteredRetryAfter(5*time.Nanosecond))

	// A normal delay is spread upward only, within +10%: a Retry-After is a
	// minimum, never an average.
	const base = 500 * time.Millisecond

	lowest, highest := base+base, time.Duration(0)

	for range 1000 {
		got := jitteredRetryAfter(base)
		assert.GreaterOrEqual(t, got, base)
		assert.LessOrEqual(t, got, base+base/10)

		lowest, highest = min(lowest, got), max(highest, got)
	}

	assert.GreaterOrEqual(t, highest-lowest, base/20, "the spread covers most of the 10% band")

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
	// should track the hint (up to +10% jitter), not the backoff, and never
	// come before it.
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
		assert.GreaterOrEqual(t, d, 500*time.Millisecond, "never retry before the Retry-After")
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

	// A 10s Retry-After is capped by the 1s MaxDelay, then spread upward, so
	// clients capped at the same MaxDelay do not all retry at the same instant.
	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", RetryAfterError(errors.New("503"), 10*time.Second)
		},
		RetryParams{
			MaxAttempts: 200,
			Strategy:    ConstantBackoff(10 * time.Millisecond),
			Hooks:       &Hooks{},
			Clock:       clk,
			Opts:        []RetryOption{MaxDelay(time.Second)},
		},
	)
	require.ErrorIs(t, err, ErrRetriesExhausted)

	durations := clk.getDurations()
	require.Len(t, durations, 199)

	lowest, highest := 2*time.Second, time.Duration(0)

	for _, d := range durations {
		assert.GreaterOrEqual(t, d, time.Second, "the capped delay is the floor")
		assert.LessOrEqual(t, d, time.Second+time.Second/10, "the jitter adds at most 10%")

		lowest, highest = min(lowest, d), max(highest, d)
	}

	assert.GreaterOrEqual(t, highest-lowest, time.Second/20, "capped delays must stay spread")
}

// An uncapped hint is spread too: clients that got the same Retry-After do
// not all retry at the same instant.
func TestDoRetryUncappedRetryAfterIsSpread(t *testing.T) {
	t.Parallel()

	clk := newImmediateTestClock()

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) {
			return "", RetryAfterError(errors.New("429"), 500*time.Millisecond)
		},
		RetryParams{
			MaxAttempts: 200,
			Strategy:    ConstantBackoff(10 * time.Millisecond),
			Hooks:       &Hooks{},
			Clock:       clk,
			Opts:        []RetryOption{MaxDelay(time.Hour)},
		},
	)
	require.ErrorIs(t, err, ErrRetriesExhausted)

	durations := clk.getDurations()
	require.Len(t, durations, 199)

	lowest, highest := time.Hour, time.Duration(0)

	for _, d := range durations {
		assert.GreaterOrEqual(t, d, 500*time.Millisecond, "never before an uncapped hint")
		assert.LessOrEqual(t, d, 550*time.Millisecond)

		lowest, highest = min(lowest, d), max(highest, d)
	}

	assert.GreaterOrEqual(t, highest-lowest, 25*time.Millisecond, "uncapped hints are spread")
}

// A strategy backoff (no Retry-After) stays a hard cap: no jitter is added
// on top of MaxDelay.
func TestDoRetryStrategyBackoffCappedExactly(t *testing.T) {
	t.Parallel()

	clk := newImmediateTestClock()

	_, err := DoRetry[string](
		context.Background(),
		func(_ context.Context) (string, error) { return "", errors.New("503") },
		RetryParams{
			MaxAttempts: 3,
			Strategy:    ConstantBackoff(10 * time.Second),
			Hooks:       &Hooks{},
			Clock:       clk,
			Opts:        []RetryOption{MaxDelay(time.Second)},
		},
	)
	require.ErrorIs(t, err, ErrRetriesExhausted)
	require.Len(t, clk.getDurations(), 2)

	for _, d := range clk.getDurations() {
		assert.Equal(t, time.Second, d, "a strategy backoff is capped exactly")
	}
}

// FuzzJitteredRetryAfter asserts the jittered delay never panics and stays
// within [d, d+10%] of the input d for any int64 duration — including values near
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
		require.GreaterOrEqual(t, got, delay, "below the Retry-After")

		// The +10%% ceiling can overflow int64; when it does, the result is
		// clamped to math.MaxInt64, which trivially satisfies the upper bound.
		if hi := delay + jitter; hi >= delay {
			require.LessOrEqual(t, got, hi, "above the +10%% ceiling")
		}
	})
}
