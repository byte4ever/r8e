package r8e

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

// reconfigureBurst resizes the bucket: a smaller one clamps the tokens at
// once, a larger one fills over time at the current rate.
func TestRateLimiterReconfigureBurst(t *testing.T) {
	t.Parallel()

	clk := newRateLimitClock(time.Now())
	rl := NewRateLimiter(10, clk, &Hooks{})

	rl.reconfigureBurst(2)
	assert.Equal(t, 2, admitted(t, rl), "shrinking clamps the full bucket")

	rl.reconfigureBurst(50)
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

func TestBurstOutOfRangePanics(t *testing.T) {
	t.Parallel()

	for _, n := range []int{0, -1, int(MaxBurst) + 1} {
		assert.PanicsWithValue(t, ErrRateLimitBurstOutOfRange, func() {
			NewRateLimiter(10, RealClock{}, &Hooks{}, Burst(n))
		}, "Burst(%d) on NewRateLimiter", n)

		assert.PanicsWithValue(t, ErrRateLimitBurstOutOfRange, func() {
			NewPolicy[string]("", WithRateLimit(10, Burst(n)))
		}, "Burst(%d) on NewPolicy", n)
	}

	assert.NotPanics(t, func() {
		NewRateLimiter(10, RealClock{}, &Hooks{}, Burst(int(MaxBurst)))
	})
}

// ---------------------------------------------------------------------------
// Burst via config (BuildOptions / Reconfigure)
// ---------------------------------------------------------------------------

func TestBuildOptionsRateLimitBurst(t *testing.T) {
	t.Parallel()

	opts, err := BuildOptions(&PolicyConfig{RateLimit: f64Ptr(100), RateLimitBurst: intPtr(2)})
	require.NoError(t, err)

	policy := NewPolicy[string]("", opts...)
	assert.Equal(t, 2, admitted(t, policy.rateLimiter))
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

	policy := NewPolicy[string]("", WithRateLimit(100, Burst(5)))
	rl := policy.rateLimiter

	require.NoError(t, policy.Reconfigure(PolicyConfig{RateLimitBurst: intPtr(2)}))
	assert.Equal(t, 2, admitted(t, rl))

	require.NoError(t, policy.Reconfigure(PolicyConfig{RateLimit: f64Ptr(1000)}))
	rl.rateMu.Lock()
	burst := rl.burst
	rl.rateMu.Unlock()
	assert.Equal(t, int64(2), burst, "a reload without the field keeps the burst")
}

// A rejected reload changes nothing, not even the fields that were valid.
func TestPolicyReconfigureRateLimitBurstRejected(t *testing.T) {
	t.Parallel()

	t.Run("out of range", func(t *testing.T) {
		t.Parallel()

		policy := NewPolicy[string]("", WithRateLimit(100, Burst(5)))

		err := policy.Reconfigure(PolicyConfig{RateLimit: f64Ptr(1), RateLimitBurst: intPtr(0)})
		require.ErrorIs(t, err, ErrRateLimitBurstOutOfRange)
		assert.InDelta(t, 100.0, policy.rateLimiter.CurrentRate(), 1e-9)
		assert.Equal(t, 5, admitted(t, policy.rateLimiter))
	})

	t.Run("no rate limiter", func(t *testing.T) {
		t.Parallel()

		policy := NewPolicy[string]("", WithBulkhead(1))

		err := policy.Reconfigure(PolicyConfig{RateLimitBurst: intPtr(2)})
		require.ErrorIs(t, err, ErrPatternAbsent)
	})
}
