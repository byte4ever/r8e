package r8e

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Two writers changing the rate at the same time must leave a bucket whose
// capacity matches the rate that won. A hot reload (Reconfigure) can race an
// AIMD step, which also publishes a new rate.
func TestRateLimiterConcurrentRateWritesKeepCapacityInStep(t *testing.T) {
	t.Parallel()

	const rounds = 20_000

	rl := NewRateLimiter(10, newRateLimitClock(time.Now()), &Hooks{})

	for range rounds {
		var (
			start sync.WaitGroup
			done  sync.WaitGroup
		)

		start.Add(1)
		done.Add(2)

		for _, rate := range []float64{100, 0.5} {
			go func() {
				defer done.Done()

				start.Wait()
				rl.Reconfigure(rate)
			}()
		}

		start.Done()
		done.Wait()

		rate := rl.CurrentRate()
		require.Equal(t, bucketCapacity(rate), rl.capacity.Load(), "rate %v", rate)
	}
}

// An AIMD step that races a Reconfigure starts from the rate it finds under
// the same lock, so it never undoes the reconfigured rate with a value
// computed from the old one.
func TestRateLimiterAIMDStepDoesNotUndoReconfigure(t *testing.T) {
	t.Parallel()

	const rounds = 5_000

	for range rounds {
		clk := newRateLimitClock(time.Now())
		rl := NewRateLimiter(100, clk, &Hooks{}, AIMD(AIMDMinRate(1), AIMDBackoff(0.5), AIMDInterval(time.Second)))
		clk.advance(time.Second)

		var (
			start sync.WaitGroup
			done  sync.WaitGroup
		)

		start.Add(1)
		done.Add(2)

		go func() {
			defer done.Done()

			start.Wait()
			rl.RecordOutcome(ErrRateLimited) // halves the rate it starts from
		}()

		go func() {
			defer done.Done()

			start.Wait()
			rl.Reconfigure(10)
		}()

		start.Done()
		done.Wait()

		// Either order is fine: the step ran first (100 → 50, then 10), or the
		// reload ran first (10 → 5). A step computed from 100 and stored after
		// the reload (50) is the lost update.
		require.Contains(t, []float64{10, 5}, rl.CurrentRate())
		require.Equal(t, bucketCapacity(rl.CurrentRate()), rl.capacity.Load())
	}
}
