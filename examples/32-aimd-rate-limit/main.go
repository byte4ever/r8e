// Package main demonstrates AIMD adaptation of the rate limiter.
//
// A plain WithRateLimit(rate) holds the refill rate fixed. Adding AIMD turns
// that rate into a starting and ceiling value: the limiter backs the rate off
// multiplicatively whenever a call comes back with a server-overload signal (an
// HTTP 429/503 Retry-After, or ErrRateLimited), then recovers it additively once
// the backend stops pushing back — the classic congestion-control sawtooth.
//
// Timeline in this example (rate=100, min=10, backoff=0.5, interval=40ms):
//   - phase 1: the backend signals overload → each interval halves the rate,
//     100 → 50 → 25 → ... down toward the 10/s floor
//   - phase 2: the backend recovers → each clean interval adds 5/s back,
//     climbing toward the 100/s ceiling
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

var errOverloaded = errors.New("backend overloaded")

func main() {
	ctx := context.Background()

	overloaded := true // flipped to false partway through to show recovery

	// The backend signals server overload with a Retry-After-carrying error,
	// exactly what an HTTP 429/503 surfaces through the httpx adapter. The
	// default AIMD classifier treats that as a backoff signal.
	backend := func(_ context.Context) (string, error) {
		if overloaded {
			return "", r8e.RetryAfterError(errOverloaded, time.Second)
		}

		return "ok", nil
	}

	policy := r8e.NewPolicy[string]("aimd-demo",
		r8e.WithRateLimit(100,
			r8e.AIMD(
				r8e.AIMDMinRate(10),
				r8e.AIMDBackoff(0.5),
				r8e.AIMDInterval(40*time.Millisecond),
				r8e.AIMDIncrease(5),
			),
		),
		r8e.WithHooks(&r8e.Hooks{
			OnRateAdapted: func(rate float64) {
				fmt.Printf("  [aimd] rate adapted → %.1f tokens/s\n", rate)
			},
		}),
	)

	fmt.Println("phase 1: backend overloaded — rate backs off")

	for range 6 {
		_, _ = policy.Do(ctx, backend) //nolint:errcheck // demo: errors drive AIMD, not handled here

		time.Sleep(45 * time.Millisecond) // cross one AIMD interval per call
	}

	fmt.Printf("  rate after backoff: %.1f tokens/s\n\n", policy.Metrics().RateLimit)

	overloaded = false

	fmt.Println("phase 2: backend recovered — rate climbs back")

	for range 6 {
		_, _ = policy.Do(ctx, backend) //nolint:errcheck // demo: errors drive AIMD, not handled here

		time.Sleep(45 * time.Millisecond)
	}

	metrics := policy.Metrics()
	fmt.Printf("  rate after recovery: %.1f tokens/s\n", metrics.RateLimit)
	fmt.Printf("  total AIMD adaptations: %d\n", metrics.RateAdaptations)
}
