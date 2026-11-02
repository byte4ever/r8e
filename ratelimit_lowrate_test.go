package r8e

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// A rate below one token per second still admits calls: the bucket holds at
// least one token, refilled at the configured rate.
func TestRateLimiterBelowOnePerSecondAdmits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		rate   float64
		period time.Duration // time to refill one token
	}{
		{name: "450 per hour", rate: 0.125, period: 8 * time.Second},
		{name: "half per second", rate: 0.5, period: 2 * time.Second},
		{name: "just under one", rate: 0.999, period: 1002 * time.Millisecond},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clk := newRateLimitClock(time.Now())
			rl := NewRateLimiter(tt.rate, clk, &Hooks{})

			require.NoError(t, rl.Allow(context.Background()), "first call")
			require.ErrorIs(t, rl.Allow(context.Background()), ErrRateLimited, "bucket empty")

			clk.advance(tt.period - 10*time.Millisecond)
			require.ErrorIs(t, rl.Allow(context.Background()), ErrRateLimited, "token not refilled yet")

			clk.advance(10 * time.Millisecond)
			require.NoError(t, rl.Allow(context.Background()), "token refilled")
		})
	}
}

// In blocking mode, a call below one token per second waits for the next
// token, not for the end of its context.
func TestRateLimiterBelowOnePerSecondBlockingWaitsForToken(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		rl := NewRateLimiter(0.125, RealClock{}, &Hooks{}, RateLimitBlocking())

		ctx, cancel := context.WithTimeout(context.Background(), time.Hour)
		defer cancel()

		require.NoError(t, rl.Allow(ctx))

		start := time.Now()

		require.NoError(t, rl.Allow(ctx))

		waited := time.Since(start)
		require.GreaterOrEqual(t, waited, 8*time.Second)
		require.Less(t, waited, 8*time.Second+10*time.Millisecond)
	})
}

// AIMD may push the rate below one token per second (its default floor is a
// tenth of the base rate). The limiter must keep admitting calls, so that their
// outcomes can bring the rate back up.
func TestRateLimiterAIMDBelowOnePerSecondRecovers(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(5, clk, &Hooks{}, AIMD())

	// One overload episode drives the rate to its default floor, 0.5/s.
	for range 30 {
		clk.advance(time.Second)
		rl.RecordOutcome(ErrRateLimited)
	}

	require.InDelta(t, 0.5, rl.CurrentRate(), 1e-9)

	// Then the downstream is healthy again. As in a policy, only an admitted
	// call records an outcome.
	admitted := 0

	for range 200 {
		clk.advance(time.Minute)

		if rl.Allow(context.Background()) == nil {
			admitted++

			rl.RecordOutcome(nil)
		}
	}

	require.Equal(t, 200, admitted)
	require.InDelta(t, 5.0, rl.CurrentRate(), 1e-9, "rate back to its base")
}

// A rate of zero or less keeps an empty bucket: no call is ever admitted.
func TestRateLimiterNonPositiveRateAdmitsNothing(t *testing.T) {
	t.Parallel()

	for _, rate := range []float64{0, -1} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			t.Parallel()

			clk := newRateLimitClock(time.Now())
			rl := NewRateLimiter(rate, clk, &Hooks{})

			require.ErrorIs(t, rl.Allow(context.Background()), ErrRateLimited)

			clk.advance(time.Hour)
			require.ErrorIs(t, rl.Allow(context.Background()), ErrRateLimited)
		})
	}
}
