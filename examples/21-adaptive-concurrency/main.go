// Example 21-adaptive-concurrency: Demonstrates the adaptive concurrency limiter
// tuning its own limit from observed latency (Netflix's Gradient2).
//
// The problem with a fixed bulkhead ceiling is that you have to guess it: set it
// too low and you throttle a healthy service, set it too high and you let an
// overloaded one melt down. This limiter guesses for you continuously — each call
// samples its round-trip time, and when latency climbs above a smoothed baseline
// (the tell-tale sign of queueing downstream) it lowers the limit, then lets it
// drift back up once things steady. This program holds the latency steady and
// watches the limit climb, then spikes latency 20x and watches it back off.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	// latency is the simulated downstream response time; it's an atomic because we
	// mutate it from main while load goroutines read it, and it's the single knob
	// that drives the limiter's whole reaction. rejected counts calls shed at the
	// limit, so we can see overload pressure as a number.
	var (
		latency  atomic.Int64
		rejected atomic.Int64
	)

	latency.Store(int64(5 * time.Millisecond))

	// InitialLimit is where we start before any RTT is observed; Min/MaxLimit are
	// the hard rails Gradient2 may never cross. The window between them is the room
	// the algorithm has to tune itself — wide enough here to see real movement.
	policy := r8e.NewPolicy[int]("downstream",
		r8e.WithHooks(&r8e.Hooks{
			OnConcurrencyRejected: func() { rejected.Add(1) },
		}),
		r8e.WithAdaptiveConcurrency(
			r8e.InitialLimit(10),
			r8e.MinLimit(2),
			r8e.MaxLimit(50),
		),
	)

	// call runs one request and reports whether it was admitted. A rejection at
	// the limit returns ErrConcurrencyLimited — that's not a failure of the
	// downstream, it's the limiter doing its job and shedding excess load, so we
	// treat it as an expected, countable outcome rather than an error to log.
	call := func() bool {
		_, err := policy.Do(context.Background(), func(_ context.Context) (int, error) {
			time.Sleep(time.Duration(latency.Load()))

			return 0, nil
		})

		return err == nil
	}

	// runLoad keeps `concurrency` callers hammering the policy for d. We need
	// genuine sustained pressure (not a handful of calls) because Gradient2 only
	// raises the limit while the limiter is actually loaded — a trickle of traffic
	// would never push it to probe higher, so we over-subscribe on purpose.
	runLoad := func(d time.Duration, concurrency int) {
		deadline := time.Now().Add(d)

		var wg sync.WaitGroup

		wg.Add(concurrency)

		for range concurrency {
			go func() {
				defer wg.Done()

				for time.Now().Before(deadline) {
					call()
				}
			}()
		}

		wg.Wait()
	}

	fmt.Printf("start: limit=%d\n", policy.Metrics().ConcurrencyLimit)

	// With a fast 5ms downstream and 40 callers fighting over a limit of 10, the
	// limiter sees stable latency and steady demand, so Gradient2 keeps drifting
	// the limit upward toward MaxLimit to admit more of the offered load.
	fmt.Println("\n=== Healthy load (5ms latency): limit climbs ===")
	runLoad(400*time.Millisecond, 40)
	fmt.Printf("  limit after healthy load: %d\n", policy.Metrics().ConcurrencyLimit)

	// Now we flip latency from 5ms to 100ms — a 20x jump that looks exactly like
	// the downstream starting to queue. The current RTT shoots above the smoothed
	// baseline, Gradient2 reads that as congestion and pulls the limit back down,
	// which in turn sheds the surplus callers (counted via OnConcurrencyRejected).
	fmt.Println("\n=== Overload (100ms latency): limit backs off ===")
	latency.Store(int64(100 * time.Millisecond))
	runLoad(time.Second, 40)

	m := policy.Metrics()
	fmt.Printf("  limit after overload:     %d\n", m.ConcurrencyLimit)
	fmt.Printf("  calls shed at the limit:  %d\n", rejected.Load())
	fmt.Printf("  in-flight now:            %d\n", m.ConcurrencyInFlight)
	fmt.Printf("  health state:             %s\n", policy.HealthStatus().State)
}
