package r8e

import (
	"context"
	"errors"
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
	refused atomic.Int64
}

func (c *cooldownCounters) hooks() *Hooks {
	return &Hooks{
		OnCoolingDown:   func() { c.refused.Add(1) },
		OnCooldownArmed: func(d time.Duration) { c.armed = append(c.armed, d) },
	}
}

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

// The cap must be positive; SetMaxCooldown ignores a non-positive one.
func TestCooldownCap(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, -time.Second} {
		assert.PanicsWithValue(t, ErrCooldownNonPositiveMax, func() { NewCooldown(MaxCooldown(d)) })
	}

	synctest.Test(t, func(t *testing.T) {
		cd := NewCooldown(MaxCooldown(time.Minute))
		cd.SetMaxCooldown(0)
		cd.SetMaxCooldown(10 * time.Second)

		d, armed := cd.observe(RetryAfterError(errQuotaHit, time.Hour))
		require.True(t, armed)
		assert.Equal(t, 10*time.Second, d)
	})
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
		assert.Equal(t, []time.Duration{time.Minute}, counters.armed)

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
		assert.Equal(t, []time.Duration{time.Minute}, counters.armed, "armed once, by the attempt")

		_, err = p.Do(t.Context(), func(context.Context) (string, error) { return "ok", nil })
		require.ErrorIs(t, err, ErrCoolingDown)
	})
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

		assert.Equal(t, []time.Duration{time.Minute}, routeA.armed)
		assert.Empty(t, routeB.armed)
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

	synctest.Test(t, func(t *testing.T) {
		limit := "30m"
		opts, err := BuildOptions(&PolicyConfig{Cooldown: &limit})
		require.NoError(t, err)

		var counters cooldownCounters

		p := NewPolicy[string]("", append(opts, WithHooks(counters.hooks()))...)

		shorter := "10m"
		require.NoError(t, p.Reconfigure(PolicyConfig{Cooldown: &shorter}))
		require.ErrorIs(t, p.Reconfigure(PolicyConfig{Cooldown: &bad}), ErrCooldownNonPositiveMax)

		_, _ = p.Do(t.Context(), func(context.Context) (string, error) {
			return "", RetryAfterError(errQuotaHit, time.Hour)
		})
		assert.Equal(t, []time.Duration{10 * time.Minute}, counters.armed)
	})

	none := NewPolicy[string]("")
	limit := "1m"
	require.ErrorIs(t, none.Reconfigure(PolicyConfig{Cooldown: &limit}), ErrPatternAbsent)

	shared := NewPolicy[string]("", WithSharedCooldown(NewCooldown()))
	require.ErrorIs(t, shared.Reconfigure(PolicyConfig{Cooldown: &limit}), ErrSharedCooldownConflict)
}
