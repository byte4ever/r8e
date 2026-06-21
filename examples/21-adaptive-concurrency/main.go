// Example 21-adaptive-concurrency: Demonstrates the adaptive concurrency limiter
// tuning its own limit from observed latency (Netflix's Gradient2): the limit
// climbs while the downstream is fast and backs off when latency rises, instead
// of a fixed bulkhead ceiling.
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
	// latency is the simulated downstream response time; we change it at runtime
	// to drive the limiter. rejected counts calls shed when at the limit.
	var (
		latency  atomic.Int64
		rejected atomic.Int64
	)

	latency.Store(int64(5 * time.Millisecond))

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

	// call runs one request and reports whether it was admitted (rejections at
	// the limit return ErrConcurrencyLimited and are expected under overload).
	call := func() bool {
		_, err := policy.Do(context.Background(), func(_ context.Context) (int, error) {
			time.Sleep(time.Duration(latency.Load()))

			return 0, nil
		})

		return err == nil
	}

	// runLoad keeps `concurrency` callers hammering the policy for d.
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

	// --- Healthy: fast downstream, the limit climbs to meet demand ---
	fmt.Println("\n=== Healthy load (5ms latency): limit climbs ===")
	runLoad(400*time.Millisecond, 40)
	fmt.Printf("  limit after healthy load: %d\n", policy.Metrics().ConcurrencyLimit)

	// --- Overload: latency spikes 20x, the limit backs off to shed load ---
	fmt.Println("\n=== Overload (100ms latency): limit backs off ===")
	latency.Store(int64(100 * time.Millisecond))
	runLoad(time.Second, 40)

	m := policy.Metrics()
	fmt.Printf("  limit after overload:     %d\n", m.ConcurrencyLimit)
	fmt.Printf("  calls shed at the limit:  %d\n", rejected.Load())
	fmt.Printf("  in-flight now:            %d\n", m.ConcurrencyInFlight)
	fmt.Printf("  health state:             %s\n", policy.HealthStatus().State)
}
