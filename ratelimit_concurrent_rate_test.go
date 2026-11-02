package r8e

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// minRacePs is the parallelism the stress tests below need to reach the
// interleavings they guard. With fewer Ps they would pass on broken code, so
// they skip instead.
const minRacePs = 4

func requireRacePs(t *testing.T) {
	t.Helper()

	if runtime.GOMAXPROCS(0) < minRacePs {
		t.Skipf("needs GOMAXPROCS >= %d to reach the racing interleaving", minRacePs)
	}
}

// race runs a and b on two goroutines released together, and waits for both.
func race(a, b func()) {
	var (
		start sync.WaitGroup
		done  sync.WaitGroup
	)

	start.Add(1)
	done.Add(2)

	for _, fn := range []func(){a, b} {
		go func() {
			defer done.Done()

			start.Wait()
			fn()
		}()
	}

	start.Done()
	done.Wait()
}

// Two writers changing the rate at the same time must leave a bucket whose
// capacity matches the rate that won. A hot reload (Reconfigure) can race an
// AIMD step, which also publishes a new rate.
func TestRateLimiterConcurrentRateWritesKeepCapacityInStep(t *testing.T) {
	t.Parallel()
	requireRacePs(t)

	const rounds = 20_000

	rl := NewRateLimiter(10, newRateLimitClock(time.Now()), &Hooks{})

	for range rounds {
		race(func() { rl.Reconfigure(100) }, func() { rl.Reconfigure(0.5) })

		rate := rl.CurrentRate()
		require.Equal(t, bucketCapacity(rate), rl.capacity.Load(), "rate %v", rate)
	}
}

// An AIMD step that races a Reconfigure starts from the rate it finds under
// the same lock: it neither undoes the reload with a value computed from the
// old rate, nor gets lost.
func TestRateLimiterAIMDStepDoesNotUndoReconfigure(t *testing.T) {
	t.Parallel()
	requireRacePs(t)

	const rounds = 5_000

	type outcome struct{ emitted, final float64 }

	// The step ran first (100 → 50, then the reload sets 10), or the reload
	// ran first (10, then the step halves it to 5).
	valid := []outcome{{emitted: 50, final: 10}, {emitted: 5, final: 5}}

	for range rounds {
		var emitted atomic.Value

		clk := newRateLimitClock(time.Now())
		hooks := &Hooks{OnRateAdapted: func(rate float64) { emitted.Store(rate) }}
		rl := NewRateLimiter(100, clk, hooks, AIMD(AIMDMinRate(1), AIMDBackoff(0.5), AIMDInterval(time.Second)))
		clk.advance(time.Second)

		race(
			func() { rl.RecordOutcome(ErrRateLimited) },
			func() { rl.Reconfigure(10) },
		)

		got, ok := emitted.Load().(float64)
		require.True(t, ok, "the AIMD step ran")
		require.Contains(t, valid, outcome{emitted: got, final: rl.CurrentRate()})
		require.Equal(t, bucketCapacity(rl.CurrentRate()), rl.capacity.Load())
	}
}

// A refill that loaded the old, larger capacity must not leave the bucket
// above a smaller capacity a concurrent rate decrease has just stored.
func TestRateLimiterRefillRacingDecreaseStaysWithinCapacity(t *testing.T) {
	t.Parallel()
	requireRacePs(t)

	const rounds = 50_000

	for range rounds {
		clk := newRateLimitClock(time.Now())
		rl := NewRateLimiter(1000, clk, &Hooks{})
		rl.tokens.Store(0)
		clk.advance(time.Second)

		var allowErr error

		race(
			func() { allowErr = rl.Allow(context.Background()) },
			func() { rl.Reconfigure(1) },
		)

		// Whatever the order, a token is there for the call: the clamp lowers
		// the bucket to the new capacity, never below it.
		require.NoError(t, allowErr)
		require.LessOrEqual(t, rl.tokens.Load(), rl.capacity.Load())
	}
}

// The OnRateAdapted hook runs with no lock held, so it may call back into the
// limiter.
func TestRateLimiterRateAdaptedHookMayReconfigure(t *testing.T) {
	t.Parallel()

	var rl *RateLimiter

	clk := newRateLimitClock(time.Now())
	hooks := &Hooks{OnRateAdapted: func(float64) { rl.Reconfigure(60) }}
	rl = NewRateLimiter(100, clk, hooks, AIMD(AIMDMinRate(1), AIMDBackoff(0.5), AIMDInterval(time.Second)))
	clk.advance(time.Second)

	done := make(chan struct{})

	go func() {
		defer close(done)

		rl.RecordOutcome(ErrRateLimited)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "RecordOutcome did not return: the hook ran under a lock")
	}

	assert.InDelta(t, 60.0, rl.CurrentRate(), 1e-9)
}
