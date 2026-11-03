package r8e

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aboveMaxBurst returns MaxBurst+1, computed at run time: on a 32-bit platform
// it wraps to a negative int, out of range all the same.
func aboveMaxBurst() int {
	maxBurst := MaxBurst

	return maxBurst + 1
}

// admitted drains the bucket without moving the clock and returns how many
// calls it admitted.
func admitted(t *testing.T, rl *RateLimiter) int {
	t.Helper()

	n := 0
	for rl.Allow(context.Background()) == nil {
		n++
		require.LessOrEqual(t, n, 10_000, "bucket never drained")
	}

	return n
}

// Burst sets the bucket apart from the rate, in both directions: a small bucket
// at a high rate and a large bucket at a low rate. The bucket starts full.
func TestRateLimiterBurstSetsCapacity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		rate  float64
		burst int
	}{
		{name: "one token at 100 per second", rate: 100, burst: 1},
		{name: "five tokens at 100 per second", rate: 100, burst: 5},
		{name: "450 tokens at 450 per hour", rate: 0.125, burst: 450},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			clk := newRateLimitClock(time.Now())
			rl := NewRateLimiter(tt.rate, clk, &Hooks{}, Burst(tt.burst))

			assert.Equal(t, tt.burst, admitted(t, rl))
		})
	}
}

// A bucket of 1 at 0.125 req/s refills one token every 8 seconds and never
// holds more than one: after a long idle period, one call leaves, not two.
func TestRateLimiterBurstOneCapsAfterIdle(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(0.125, clk, &Hooks{}, Burst(1))

	require.Equal(t, 1, admitted(t, rl))

	clk.advance(time.Hour)
	assert.Equal(t, 1, admitted(t, rl))
}

// Reconfigure and the AIMD step change the rate, never a capacity set by Burst.
func TestRateLimiterBurstSurvivesRateChanges(t *testing.T) {
	t.Parallel()

	t.Run("reconfigure", func(t *testing.T) {
		t.Parallel()

		clk := newRateLimitClock(time.Now())
		rl := NewRateLimiter(100, clk, &Hooks{}, Burst(3))

		rl.Reconfigure(1000)
		clk.advance(time.Hour)
		assert.Equal(t, 3, admitted(t, rl), "a higher rate keeps the bucket")

		rl.Reconfigure(0.5)
		clk.advance(time.Hour)
		assert.Equal(t, 3, admitted(t, rl), "a rate below 1 keeps the bucket")
	})

	t.Run("aimd", func(t *testing.T) {
		t.Parallel()

		clk := newRateLimitClock(time.Now())
		rl := NewRateLimiter(100, clk, &Hooks{}, Burst(3), AIMD())

		for range 5 {
			clk.advance(2 * time.Second)
			rl.RecordOutcome(ErrRateLimited)
		}

		require.Less(t, rl.CurrentRate(), 100.0, "the AIMD step lowered the rate")

		clk.advance(time.Hour)
		assert.Equal(t, 3, admitted(t, rl))
	})
}

// A burst reload resizes the bucket: a smaller one clamps the tokens at once,
// a larger one fills over time at the current rate.
func TestRateLimiterReloadBurst(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(10, clk, &Hooks{}, Burst(10))

	two, fifty := 2, 50

	rl.applyReload(nil, &two)
	assert.Equal(t, 2, admitted(t, rl), "shrinking clamps the full bucket")

	rl.applyReload(nil, &fifty)
	assert.Equal(t, 0, admitted(t, rl), "growing adds no token at once")

	clk.advance(10 * time.Second)
	assert.Equal(t, 50, admitted(t, rl), "the larger bucket fills over time")

	rl.Reconfigure(1000)
	clk.advance(time.Second)
	assert.Equal(t, 50, admitted(t, rl), "a later rate change keeps the reloaded burst")
}

// A rate of zero or less admits nothing, Burst or not.
func TestRateLimiterBurstWithNonPositiveRateAdmitsNothing(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(0, clk, &Hooks{}, Burst(10))

	clk.advance(time.Hour)
	assert.Equal(t, 0, admitted(t, rl))
}

// A zero rate empties a Burst bucket; a positive rate brings the Burst
// capacity back, but not the tokens: the bucket refills over time.
func TestRateLimiterBurstAfterZeroRate(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{}, Burst(5))

	rl.Reconfigure(0)
	rl.Reconfigure(100)
	assert.Equal(t, 0, admitted(t, rl), "the zero rate emptied the bucket")

	clk.advance(time.Second)
	assert.Equal(t, 5, admitted(t, rl), "the Burst capacity is back")
}

// A full bucket of MaxBurst tokens refills without overflowing: it keeps
// admitting calls after an idle period.
func TestRateLimiterMaxBurstRefills(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(100, clk, &Hooks{}, Burst(MaxBurst))

	for _, idle := range []time.Duration{10 * time.Millisecond, time.Second, time.Hour} {
		clk.advance(idle)
		require.NoError(t, rl.Allow(context.Background()), "after %s idle", idle)
		assert.Positive(t, rl.tokens.Load(), "after %s idle", idle)
	}
}

// A long idle period at a high rate adds more tokens than an int64 can hold:
// the refill caps at the bucket instead of wrapping.
func TestRateLimiterRefillCapsLongIdleAtHighRate(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(1e12, clk, &Hooks{}, Burst(3))

	require.Equal(t, 3, admitted(t, rl))

	clk.advance(1000 * time.Hour)
	assert.Equal(t, 3, admitted(t, rl))
}

func TestBurstOutOfRangePanics(t *testing.T) {
	t.Parallel()

	// Built at run time, so that MaxBurst+1 wraps to a negative value on a
	// 32-bit platform instead of failing to compile; out of range either way.
	maxBurst := MaxBurst

	for _, n := range []int{0, -1, maxBurst + 1} {
		t.Run(fmt.Sprintf("Burst(%d)", n), func(t *testing.T) {
			t.Parallel()

			assert.PanicsWithValue(t, ErrRateLimitBurstOutOfRange, func() {
				NewRateLimiter(10, RealClock{}, &Hooks{}, Burst(n))
			}, "on NewRateLimiter")

			assert.PanicsWithValue(t, ErrRateLimitBurstOutOfRange, func() {
				NewPolicy[string]("", WithRateLimit(10, Burst(n)))
			}, "on NewPolicy")
		})
	}

	assert.NotPanics(t, func() {
		NewRateLimiter(10, RealClock{}, &Hooks{}, Burst(MaxBurst))
	})
}

// ---------------------------------------------------------------------------
// Burst via config (BuildOptions / Reconfigure)
// ---------------------------------------------------------------------------

func TestBuildOptionsRateLimitBurst(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  PolicyConfig
	}{
		{name: "burst", cfg: PolicyConfig{RateLimit: f64Ptr(100), RateLimitBurst: intPtr(2)}},
		{
			name: "burst with aimd",
			cfg: PolicyConfig{
				RateLimit:      f64Ptr(100),
				RateLimitBurst: intPtr(2),
				AIMD:           &AIMDConfig{},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts, err := BuildOptions(&tt.cfg)
			require.NoError(t, err)

			clk := newRateLimitClock(time.Now())
			policy := NewPolicy[string]("", append(opts, WithClock(clk))...)
			assert.Equal(t, 2, admitted(t, policy.rateLimiter))
		})
	}
}

func TestBuildOptionsRateLimitBurstRejected(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		cfg     PolicyConfig
		wantErr error
	}{
		{
			name:    "without rate_limit",
			cfg:     PolicyConfig{RateLimitBurst: intPtr(2)},
			wantErr: ErrBurstWithoutRateLimit,
		},
		{
			name:    "zero",
			cfg:     PolicyConfig{RateLimit: f64Ptr(100), RateLimitBurst: intPtr(0)},
			wantErr: ErrRateLimitBurstOutOfRange,
		},
		{
			name:    "negative",
			cfg:     PolicyConfig{RateLimit: f64Ptr(100), RateLimitBurst: intPtr(-3)},
			wantErr: ErrRateLimitBurstOutOfRange,
		},
		{
			name:    "above MaxBurst",
			cfg:     PolicyConfig{RateLimit: f64Ptr(100), RateLimitBurst: intPtr(aboveMaxBurst())},
			wantErr: ErrRateLimitBurstOutOfRange,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := BuildOptions(&tt.cfg)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}

// A reload that carries rate_limit_burst resizes the bucket; a reload without
// it leaves the bucket as it is, like every other absent field.
func TestPolicyReconfigureRateLimitBurst(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	policy := NewPolicy[string]("", WithRateLimit(100, Burst(5)), WithClock(clk))
	rl := policy.rateLimiter

	require.NoError(t, policy.Reconfigure(PolicyConfig{RateLimitBurst: intPtr(2)}))
	assert.Equal(t, 2, admitted(t, rl))

	require.NoError(t, policy.Reconfigure(PolicyConfig{RateLimit: f64Ptr(1000)}))
	clk.advance(time.Hour)
	assert.Equal(t, 2, admitted(t, rl), "a reload without the field keeps the burst")
}

// A reload that carries a rate and a burst applies both at once: a full bucket
// keeps its tokens instead of being clamped to the old burst at the new rate
// first.
func TestPolicyReconfigureRateAndBurstTogether(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	policy := NewPolicy[string]("", WithRateLimit(100, Burst(100)), WithClock(clk))

	require.NoError(t, policy.Reconfigure(PolicyConfig{
		RateLimit:      f64Ptr(0.5),
		RateLimitBurst: intPtr(450),
	}))

	assert.Equal(t, 100, admitted(t, policy.rateLimiter))
	assert.InDelta(t, 0.5, policy.rateLimiter.CurrentRate(), 1e-9)
}

// A rejected reload changes nothing, not even the fields that were valid.
func TestPolicyReconfigureRateLimitBurstRejected(t *testing.T) {
	t.Parallel()

	for _, burst := range []int{0, aboveMaxBurst()} {
		t.Run(fmt.Sprintf("out of range %d", burst), func(t *testing.T) {
			t.Parallel()

			clk := newRateLimitClock(time.Now())
			policy := NewPolicy[string]("", WithRateLimit(100, Burst(5)), WithClock(clk))

			err := policy.Reconfigure(PolicyConfig{RateLimit: f64Ptr(1), RateLimitBurst: intPtr(burst)})
			require.ErrorIs(t, err, ErrRateLimitBurstOutOfRange)
			assert.InDelta(t, 100.0, policy.rateLimiter.CurrentRate(), 1e-9)
			assert.Equal(t, 5, admitted(t, policy.rateLimiter))
		})
	}

	// A valid burst next to a field that fails is not applied either.
	t.Run("valid burst, invalid aimd", func(t *testing.T) {
		t.Parallel()

		clk := newRateLimitClock(time.Now())
		policy := NewPolicy[string]("", WithRateLimit(100, Burst(5)), WithClock(clk))

		err := policy.Reconfigure(PolicyConfig{
			RateLimitBurst: intPtr(2),
			AIMD:           &AIMDConfig{Backoff: f64Ptr(0.9)},
		})
		require.ErrorIs(t, err, ErrAIMDWithoutRateLimit)
		assert.Equal(t, 5, admitted(t, policy.rateLimiter))
	})

	t.Run("no rate limiter", func(t *testing.T) {
		t.Parallel()

		policy := NewPolicy[string]("", WithBulkhead(1))

		err := policy.Reconfigure(PolicyConfig{RateLimitBurst: intPtr(2)})
		require.ErrorIs(t, err, ErrPatternAbsent)
		assert.ErrorContains(t, err, "rate_limit")
	})

	// A reload retunes a burst, it cannot add one: a rolled-back file would
	// otherwise leave the live policy with a bucket the file no longer has.
	t.Run("rate limiter built without Burst", func(t *testing.T) {
		t.Parallel()

		clk := newRateLimitClock(time.Now())
		policy := NewPolicy[string]("", WithRateLimit(100), WithClock(clk))

		err := policy.Reconfigure(PolicyConfig{RateLimit: f64Ptr(5), RateLimitBurst: intPtr(2)})
		require.ErrorIs(t, err, ErrPatternAbsent)
		assert.ErrorContains(t, err, "rate_limit_burst")
		assert.InDelta(t, 100.0, policy.rateLimiter.CurrentRate(), 1e-9)
		assert.Equal(t, 100, admitted(t, policy.rateLimiter))
	})
}
