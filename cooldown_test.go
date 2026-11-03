package r8e

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errQuotaHit = errors.New("429 quota exceeded")
	errNoQuota  = errors.New("503 unavailable")
)

// cooldownCounters counts the cooldown events one set of hooks receives.
type cooldownCounters struct {
	armed   []time.Duration
	mu      sync.Mutex
	refused atomic.Int64
}

func (c *cooldownCounters) hooks() *Hooks {
	return &Hooks{
		OnCoolingDown: func() { c.refused.Add(1) },
		OnCooldownArmed: func(d time.Duration) {
			c.mu.Lock()
			defer c.mu.Unlock()

			c.armed = append(c.armed, d)
		},
	}
}

// arms returns the durations the cooldown was armed for, in order.
func (c *cooldownCounters) arms() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]time.Duration(nil), c.armed...)
}

// settableClock is a clock whose time only moves when the test says so, and,
// with tick set, by one nanosecond on every Now.
type settableClock struct {
	now  time.Time
	mu   sync.Mutex
	tick bool
}

func newSettableClock() *settableClock {
	return &settableClock{now: time.Unix(1_000_000, 0)}
}

func (c *settableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.tick {
		c.now = c.now.Add(time.Nanosecond)
	}

	return c.now
}

func (c *settableClock) Since(t time.Time) time.Duration { return c.Now().Sub(t) }

//nolint:ireturn // satisfies the Timer interface
func (*settableClock) NewTimer(d time.Duration) Timer { return RealClock{}.NewTimer(d) }

func (c *settableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// hinted fails with a one-minute Retry-After hint.
func hinted(context.Context) (string, error) {
	return "", RetryAfterError(errQuotaHit, time.Minute)
}

// succeed returns at once with no error.
func succeed(context.Context) (string, error) { return "ok", nil }

// A hint arms the cooldown until now plus the hint, capped by MaxCooldown; a
// later, shorter hint never shortens it, and an error with no hint or one
// CooldownIf rejects leaves it alone.
func TestCooldownObserve(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		cd := NewCooldown(MaxCooldown(time.Hour), CooldownIf(func(err error) bool {
			return !errors.Is(err, errNoQuota)
		}))

		assert.False(t, cd.Armed())

		_, armed := cd.observe(errQuotaHit)
		assert.False(t, armed, "no hint, no cooldown")

		_, armed = cd.observe(RetryAfterError(errNoQuota, time.Minute))
		assert.False(t, armed, "CooldownIf rejects it")

		d, armed := cd.observe(RetryAfterError(errQuotaHit, 10*time.Minute))
		require.True(t, armed)
		assert.Equal(t, 10*time.Minute, d)
		assert.Equal(t, 10*time.Minute, cd.Remaining())

		_, armed = cd.observe(RetryAfterError(errQuotaHit, time.Minute))
		assert.False(t, armed, "a shorter hint does not move the deadline")
		assert.Equal(t, 10*time.Minute, cd.Remaining())

		d, armed = cd.observe(RetryAfterError(errQuotaHit, 72*time.Hour))
		require.True(t, armed)
		assert.Equal(t, time.Hour, d, "capped by MaxCooldown")

		time.Sleep(time.Hour)
		assert.False(t, cd.Armed(), "disarmed once the deadline passes")
		assert.Zero(t, cd.Remaining())
	})
}

// The cap must be positive: NewCooldown and NewPolicy panic on a non-positive
// one, and SetMaxCooldown rejects it and keeps the cap it had.
func TestCooldownCap(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -time.Second} {
		assert.PanicsWithValue(t, ErrCooldownNonPositiveMax, func() { NewCooldown(MaxCooldown(d)) }, d)
		assert.PanicsWithValue(t, ErrCooldownNonPositiveMax, func() {
			NewPolicy[string]("", WithCooldown(MaxCooldown(d)))
		}, d)
	}

	synctest.Test(t, func(t *testing.T) {
		cd := NewCooldown(MaxCooldown(time.Minute))
		require.ErrorIs(t, cd.SetMaxCooldown(0), ErrCooldownNonPositiveMax)

		d, armed := cd.observe(RetryAfterError(errQuotaHit, time.Hour))
		require.True(t, armed)
		assert.Equal(t, time.Minute, d, "the rejected cap leaves the old one")

		time.Sleep(time.Minute)
		require.NoError(t, cd.SetMaxCooldown(10*time.Second))

		d, armed = cd.observe(RetryAfterError(errQuotaHit, time.Hour))
		require.True(t, armed)
		assert.Equal(t, 10*time.Second, d)
	})
}

// Without MaxCooldown, one hint arms the cooldown for one hour at most, on a
// standalone cooldown and on a policy's own.
func TestCooldownDefaultCap(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		d, armed := NewCooldown().observe(RetryAfterError(errQuotaHit, 72*time.Hour))
		require.True(t, armed)
		assert.Equal(t, time.Hour, d, "standalone")

		var counters cooldownCounters

		p := NewPolicy[string]("", WithCooldown(), WithHooks(counters.hooks()))
		_, _ = p.Do(t.Context(), func(context.Context) (string, error) {
			return "", RetryAfterError(errQuotaHit, 72*time.Hour)
		})
		assert.Equal(t, []time.Duration{time.Hour}, counters.arms(), "policy")
	})
}

// A nil CooldownClock is ignored, so the cooldown keeps a working clock.
func TestCooldownClockNil(t *testing.T) {
	t.Parallel()

	assert.False(t, NewCooldown(CooldownClock(nil)).Armed())

	v, err := NewPolicy[string]("", WithCooldown(CooldownClock(nil))).Do(t.Context(), succeed)
	require.NoError(t, err)
	assert.Equal(t, "ok", v)
}

// WithCooldown reads the policy's clock, even when given a CooldownClock:
// moving the policy clock past the deadline disarms it.
func TestWithCooldownReadsThePolicyClock(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*settableClock) []CooldownOption{
		"no CooldownClock":    func(*settableClock) []CooldownOption { return nil },
		"other CooldownClock": func(*settableClock) []CooldownOption { return []CooldownOption{CooldownClock(newSettableClock())} },
	}

	for name, cooldownOpts := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			clock := newSettableClock()
			p := NewPolicy[string]("", WithClock(clock), WithCooldown(cooldownOpts(clock)...))

			_, err := p.Do(t.Context(), hinted)
			require.ErrorIs(t, err, errQuotaHit)

			_, err = p.Do(t.Context(), succeed)
			require.ErrorIs(t, err, ErrCoolingDown, "armed on the policy clock")

			clock.advance(2 * time.Minute)

			_, err = p.Do(t.Context(), succeed)
			require.NoError(t, err, "the policy clock moved past the deadline")
		})
	}
}

// Without a retry, a call whose error carries a hint arms the cooldown; the
// next calls are refused locally with ErrCoolingDown until the deadline.
func TestWithCooldownRefusesCallsUntilTheDeadline(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var counters cooldownCounters

		p := NewPolicy[string]("", WithCooldown(), WithHooks(counters.hooks()))

		var calls atomic.Int64

		quota := func(context.Context) (string, error) {
			calls.Add(1)

			return "", RetryAfterError(errQuotaHit, time.Minute)
		}

		_, err := p.Do(t.Context(), quota)
		require.ErrorIs(t, err, errQuotaHit)

		for range 3 {
			_, err = p.Do(t.Context(), quota)
			require.ErrorIs(t, err, ErrCoolingDown)
			assert.True(t, IsLocalRejection(err))
		}

		assert.Equal(t, int64(1), calls.Load(), "refused calls never leave")
		assert.Equal(t, int64(3), counters.refused.Load())
		assert.Equal(t, int64(3), p.Metrics().CoolingDown)
		assert.Equal(t, []time.Duration{time.Minute}, counters.arms())

		time.Sleep(time.Minute)

		v, err := p.Do(t.Context(), func(context.Context) (string, error) { return "ok", nil })
		require.NoError(t, err)
		assert.Equal(t, "ok", v)
	})
}

// A success does not disarm the cooldown: a call already in flight when it
// arms comes back fine, and the next call is still refused.
func TestWithCooldownSuccessDoesNotDisarm(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := NewPolicy[string]("", WithCooldown())

		release := make(chan struct{})
		done := make(chan error, 1)

		go func() {
			_, err := p.Do(t.Context(), func(context.Context) (string, error) {
				<-release

				return "late success", nil
			})
			done <- err
		}()

		synctest.Wait()

		_, err := p.Do(t.Context(), func(context.Context) (string, error) {
			return "", RetryAfterError(errQuotaHit, time.Minute)
		})
		require.ErrorIs(t, err, errQuotaHit)

		close(release)
		require.NoError(t, <-done)

		_, err = p.Do(t.Context(), func(context.Context) (string, error) { return "ok", nil })
		require.ErrorIs(t, err, ErrCoolingDown)
	})
}

// Inside a retry, an attempt whose hint arms the cooldown ends the loop: no
// other attempt leaves, and the server's error comes back as is.
func TestWithCooldownStopsTheRetry(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var counters cooldownCounters

		p := NewPolicy[string]("",
			WithCooldown(),
			WithRetry(3, ConstantBackoff(time.Millisecond)),
			WithHooks(counters.hooks()))

		want := RetryAfterError(errQuotaHit, time.Minute)

		var calls atomic.Int64

		_, err := p.Do(t.Context(), func(context.Context) (string, error) {
			calls.Add(1)

			return "", want
		})

		assert.Equal(t, int64(1), calls.Load())
		assert.Equal(t, want, err, "the server's error, as is")
		require.NotErrorIs(t, err, ErrRetriesExhausted)
		assert.Equal(t, []time.Duration{time.Minute}, counters.arms(), "armed once, by the attempt")

		_, err = p.Do(t.Context(), func(context.Context) (string, error) { return "ok", nil })
		require.ErrorIs(t, err, ErrCoolingDown)
	})
}

// With a retry, only the retry loop arms the cooldown: on a clock that moves
// between the attempt and the end of the call, an arming entry would extend
// the deadline a second time.
func TestWithCooldownArmsOncePerHint(t *testing.T) {
	t.Parallel()

	clock := newSettableClock()
	clock.tick = true

	var counters cooldownCounters

	p := NewPolicy[string]("", WithClock(clock), WithCooldown(),
		WithRetry(3, ConstantBackoff(0)), WithHooks(counters.hooks()))

	_, err := p.Do(t.Context(), hinted)
	require.ErrorIs(t, err, errQuotaHit)
	assert.Len(t, counters.arms(), 1)
}

// A hint arms the cooldown even when the retry stops for another reason: a
// permanent error, or a RetryIf that rejects it.
func TestWithCooldownArmsOnANonRetryableHint(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		call      func(context.Context) (string, error)
		retryOpts []RetryOption
	}{
		"permanent": {
			call: func(context.Context) (string, error) {
				return "", Permanent(RetryAfterError(errQuotaHit, time.Minute))
			},
		},
		"RetryIf rejects it": {
			call:      hinted,
			retryOpts: []RetryOption{RetryIf(func(err error) bool { return !errors.Is(err, errQuotaHit) })},
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				var counters cooldownCounters

				p := NewPolicy[string]("", WithCooldown(), WithHooks(counters.hooks()),
					WithRetry(3, ConstantBackoff(time.Millisecond), tt.retryOpts...))

				_, err := p.Do(t.Context(), tt.call)
				require.ErrorIs(t, err, errQuotaHit)
				assert.Equal(t, []time.Duration{time.Minute}, counters.arms())

				_, err = p.Do(t.Context(), succeed)
				require.ErrorIs(t, err, ErrCoolingDown)
			})
		})
	}
}

// A cooldown armed by another policy while a retry sleeps its backoff stops
// that retry: no attempt leaves, and the last error comes back as is.
func TestWithCooldownStopsARetryArmedFromOutside(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		quota := NewCooldown()
		other := NewPolicy[string]("", WithSharedCooldown(quota))
		retrying := NewPolicy[string]("", WithSharedCooldown(quota),
			WithRetry(3, ConstantBackoff(time.Second)))

		var calls atomic.Int64

		done := make(chan error, 1)

		go func() {
			_, err := retrying.Do(t.Context(), func(context.Context) (string, error) {
				calls.Add(1)

				return "", errNoQuota
			})
			done <- err
		}()

		// The first attempt failed with no hint; the retry sleeps its backoff.
		synctest.Wait()

		_, err := other.Do(t.Context(), hinted)
		require.ErrorIs(t, err, errQuotaHit)

		err = <-done
		assert.Equal(t, errNoQuota, err, "the last error, as is")
		assert.Equal(t, int64(1), calls.Load(), "no retry left while armed")
	})
}

// A hint on the last allowed attempt comes back as is too, not wrapped with
// ErrRetriesExhausted.
func TestWithCooldownHintOnTheLastAttempt(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := NewPolicy[string]("", WithCooldown(), WithRetry(2, ConstantBackoff(time.Millisecond)))

		var calls atomic.Int64

		_, err := p.Do(t.Context(), func(context.Context) (string, error) {
			if calls.Add(1) == 1 {
				return "", errNoQuota
			}

			return "", RetryAfterError(errQuotaHit, time.Minute)
		})

		require.ErrorIs(t, err, errQuotaHit)
		require.NotErrorIs(t, err, ErrRetriesExhausted)
		assert.Equal(t, int64(2), calls.Load())
	})
}

// One cooldown is safe for concurrent use by every policy sharing it.
func TestCooldownConcurrentUse(t *testing.T) {
	t.Parallel()

	cd := NewCooldown(MaxCooldown(time.Minute))

	var wg sync.WaitGroup

	for i := range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 200 {
				cd.observe(RetryAfterError(errQuotaHit, time.Duration(i+1)*time.Second))
				_ = cd.Armed()
				_ = cd.Remaining()
				_ = cd.SetMaxCooldown(time.Duration(i+1) * time.Minute)
			}
		}()
	}

	wg.Wait()
	assert.True(t, cd.Armed())
}

// ValidateOptions reports the conflict NewPolicy would panic with.
func TestValidateOptions(t *testing.T) {
	t.Parallel()

	require.NoError(t, ValidateOptions(WithCooldown()))
	require.ErrorIs(t, ValidateOptions(WithSharedCooldown(NewCooldown()), WithCooldown()), ErrSharedCooldownConflict)
}

// Refused calls never reach the breaker: it counts only the server's own
// error and stays closed through the cooldown.
func TestWithCooldownKeepsRefusalsFromTheBreaker(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := NewPolicy[string]("",
			WithCooldown(),
			WithCircuitBreaker(FailureThreshold(2)))

		_, err := p.Do(t.Context(), func(context.Context) (string, error) {
			return "", RetryAfterError(errQuotaHit, time.Minute)
		})
		require.ErrorIs(t, err, errQuotaHit)

		for range 10 {
			_, err = p.Do(t.Context(), func(context.Context) (string, error) { return "", nil })
			require.ErrorIs(t, err, ErrCoolingDown)
		}

		assert.Equal(t, CircuitClosed, p.circuitBreaker.State())
	})
}

// One shared cooldown stops every policy using it; each reports its own
// refusals and arms.
func TestWithSharedCooldown(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var routeA, routeB cooldownCounters

		quota := NewCooldown()
		a := NewPolicy[string]("", WithSharedCooldown(quota), WithHooks(routeA.hooks()))
		b := NewPolicy[string]("", WithSharedCooldown(quota), WithHooks(routeB.hooks()))

		_, err := a.Do(t.Context(), func(context.Context) (string, error) {
			return "", RetryAfterError(errQuotaHit, time.Minute)
		})
		require.ErrorIs(t, err, errQuotaHit)

		_, err = b.Do(t.Context(), func(context.Context) (string, error) { return "ok", nil })
		require.ErrorIs(t, err, ErrCoolingDown, "the other route is stopped too")

		assert.Equal(t, []time.Duration{time.Minute}, routeA.arms())
		assert.Empty(t, routeB.arms())
		assert.Zero(t, routeA.refused.Load())
		assert.Equal(t, int64(1), routeB.refused.Load())
	})
}

// A policy cannot have both its own cooldown and a shared one.
func TestWithSharedCooldownConflicts(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t, ErrSharedCooldownConflict, func() {
		NewPolicy[string]("", WithSharedCooldown(NewCooldown()), WithCooldown())
	})
}

// An armed cooldown makes the policy degraded, never critical.
func TestWithCooldownHealth(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		p := NewPolicy[string]("", WithRegistry(NewRegistry()), WithCooldown())

		assert.True(t, p.HealthStatus().Healthy)

		_, _ = p.Do(t.Context(), func(context.Context) (string, error) {
			return "", RetryAfterError(errQuotaHit, time.Minute)
		})

		status := p.HealthStatus()
		assert.Contains(t, status.Conditions, ConditionCoolingDown)
		assert.Equal(t, CriticalityDegraded, status.Criticality)

		time.Sleep(time.Minute)
		assert.True(t, p.HealthStatus().Healthy)
	})
}

// The cooldown config value builds the pattern with its cap, and a reload
// changes the cap of the policy's own cooldown only.
func TestCooldownConfig(t *testing.T) {
	t.Parallel()

	bad := "0s"
	_, err := BuildOptions(&PolicyConfig{Cooldown: &bad})
	require.ErrorIs(t, err, ErrCooldownNonPositiveMax)
	assert.ErrorContains(t, err, "cooldown")

	unparsable := "1 hour"
	_, err = BuildOptions(&PolicyConfig{Cooldown: &unparsable})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrCooldownNonPositiveMax)
	assert.ErrorContains(t, err, "cooldown")
	assert.ErrorContains(t, err, `"1 hour"`, "the error names the bad value")

	synctest.Test(t, func(t *testing.T) {
		limit := "30m"
		opts, err := BuildOptions(&PolicyConfig{Cooldown: &limit})
		require.NoError(t, err)

		var counters cooldownCounters

		p := NewPolicy[string]("", append(opts, WithHooks(counters.hooks()))...)

		shorter := "10m"
		require.NoError(t, p.Reconfigure(PolicyConfig{Cooldown: &shorter}))
		require.ErrorIs(t, p.Reconfigure(PolicyConfig{Cooldown: &bad}), ErrCooldownNonPositiveMax)

		err = p.Reconfigure(PolicyConfig{Cooldown: &unparsable})
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrCooldownNonPositiveMax)
		assert.ErrorContains(t, err, "cooldown")
		assert.ErrorContains(t, err, `"1 hour"`, "the error names the bad value")

		_, _ = p.Do(t.Context(), func(context.Context) (string, error) {
			return "", RetryAfterError(errQuotaHit, time.Hour)
		})
		assert.Equal(t, []time.Duration{10 * time.Minute}, counters.arms())
	})

	none := NewPolicy[string]("")
	limit := "1m"
	require.ErrorIs(t, none.Reconfigure(PolicyConfig{Cooldown: &limit}), ErrPatternAbsent)

	shared := NewPolicy[string]("", WithSharedCooldown(NewCooldown()))
	require.ErrorIs(t, shared.Reconfigure(PolicyConfig{Cooldown: &limit}), ErrSharedCooldownConflict)
}
