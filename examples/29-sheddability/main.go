// Example 29-criticality: Demonstrates per-call load-shedding priority with
// WithSheddability.
//
// The problem: when a backend is overloaded, a throttler that sheds blindly will
// drop user-facing requests and background jobs with equal probability — exactly
// the wrong outcome, since the cheap, deferrable work is what should yield first.
//
// WithSheddability lets each call declare its priority on the context. When the
// adaptive throttler starts shedding, calls marked SheddabilityAlways (background
// work) are dropped first, calls marked SheddabilityNever (user-facing, critical)
// always reach the backend, and unannotated calls (SheddabilityDefault) are shed
// at the normal SRE probability. The example bursts all three classes against a
// healthy, then overloaded, then recovered backend so the priority ordering is
// visible in the pass rates.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/byte4ever/r8e"
)

var errBackend = errors.New("backend overloaded")

func main() {
	var (
		healthy    atomic.Bool
		shed       atomic.Int64
		critPassed atomic.Int64
	)

	healthy.Store(true)

	// The adaptive throttler watches the success/failure ratio over a rolling
	// window and starts rejecting locally (before the call even leaves the
	// process) once accepts outrun backend capacity — the Google SRE
	// client-side throttling model. The knobs are tuned tight so the demo reacts
	// within a couple of windows: OverloadRatio(2) tolerates 2 accepts per
	// backend success, MinRequests(5) avoids shedding on tiny samples, the 2s
	// window keeps the loop short, and MaxRejectionRate(0.9) still lets a
	// trickle through so the throttler can sense recovery. OnThrottled counts
	// every local rejection.
	policy := r8e.NewPolicy[string]("api",
		r8e.WithHooks(&r8e.Hooks{
			OnThrottled: func() { shed.Add(1) },
		}),
		r8e.WithAdaptiveThrottle(
			r8e.OverloadRatio(2),
			r8e.MinRequests(5),
			r8e.ThrottleWindow(2*time.Second),
			r8e.MaxRejectionRate(0.9),
		),
	)

	// call issues one request with the given context and returns whether fn ran.
	// A locally-shed call never invokes fn and comes back as ErrThrottled, so the
	// absence of that error is exactly "the call reached the backend".
	call := func(ctx context.Context) (reached bool) {
		_, err := policy.Do(ctx, func(_ context.Context) (string, error) {
			if healthy.Load() {
				return "ok", nil
			}

			return "", errBackend
		})

		return !errors.Is(err, r8e.ErrThrottled)
	}

	burst := func(label string, count int) {
		shed.Store(0)
		critPassed.Store(0)

		// One context per priority class, stamped once and reused across the
		// burst. The default context carries no stamp on purpose — that is what a
		// caller who never thought about sheddability passes, and it gets the
		// baseline SRE probability.
		bgCtx := r8e.WithSheddability(context.Background(), r8e.SheddabilityAlways)
		critCtx := r8e.WithSheddability(context.Background(), r8e.SheddabilityNever)
		defCtx := context.Background()

		var bgPassed, defPassed, critCount int

		// Interleave the three classes round-robin so they all face the same
		// throttler state at the same time — otherwise whichever class ran first
		// would shape the window the others see.
		for i := range count {
			switch i % 3 {
			case 0: // background — shed first
				if call(bgCtx) {
					bgPassed++
				}
			case 1: // default — normal probability
				if call(defCtx) {
					defPassed++
				}
			case 2: // critical — never shed
				if call(critCtx) {
					critCount++

					critPassed.Add(1)
				}
			default:
				// unreachable: i%3 is always 0, 1, or 2
			}
		}

		fmt.Printf("%s\n", label)
		fmt.Printf("  background (sheddable): %d/%d reached backend\n", bgPassed, count/3)
		fmt.Printf("  default:                %d/%d reached backend\n", defPassed, count/3)
		fmt.Printf("  critical (never-shed):  %d/%d reached backend\n", critCount, count/3)
		fmt.Printf("  total shed locally:     %d\n\n", shed.Load())
	}

	// Healthy backend: every success feeds the window, the throttler sees plenty
	// of capacity, and all three classes pass untouched — the baseline.
	burst("=== Healthy backend: no shedding ===", 99)

	// Overloaded: flip the backend to failing and let one full throttle window
	// elapse first, so the rolling ratio reflects the new (bad) reality before
	// the burst. Now the priority ordering shows: background sheds first,
	// critical still gets through.
	time.Sleep(2500 * time.Millisecond)
	healthy.Store(false)
	burst("=== Overloaded backend: shedding by priority ===", 99)

	// Recovery: restore the backend and again wait a window so successes can
	// refill the ratio; the throttler then reopens and shedding clears for all
	// classes, proving the mechanism is adaptive rather than sticky.
	healthy.Store(true)
	time.Sleep(2500 * time.Millisecond)
	burst("=== Recovered: shedding clears ===", 99)
}
