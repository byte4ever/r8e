// Example 29-criticality: Demonstrates per-call load-shedding priority with
// WithSheddability. When the backend is struggling, the adaptive throttler sheds
// calls that are marked SheddabilityAlways (background work) first, while calls
// marked SheddabilityNever (user-facing, critical) always reach the backend.
// Calls with no annotation (SheddabilityDefault) are shed probabilistically.
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

		bgCtx := r8e.WithSheddability(context.Background(), r8e.SheddabilityAlways)
		critCtx := r8e.WithSheddability(context.Background(), r8e.SheddabilityNever)
		defCtx := context.Background()

		var bgPassed, defPassed, critCount int

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

	// --- Healthy: all three classes pass ---
	burst("=== Healthy backend: no shedding ===", 99)

	// --- Overloaded: background shed first, critical always passes ---
	time.Sleep(2500 * time.Millisecond)
	healthy.Store(false)
	burst("=== Overloaded backend: shedding by priority ===", 99)

	// --- Recovery ---
	healthy.Store(true)
	time.Sleep(2500 * time.Millisecond)
	burst("=== Recovered: shedding clears ===", 99)
}
