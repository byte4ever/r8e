// Example 06-bulkhead: Demonstrates concurrency limiting with the bulkhead
// pattern.
//
// A bulkhead caps how many calls run at once, so one slow dependency can't
// soak up every goroutine or connection and starve the rest of the system
// (the namesake: sealed compartments stop one flooded section from sinking the
// ship). Unlike a rate limiter it bounds simultaneous occupancy, not calls per
// second: excess callers aren't queued, they're rejected immediately with
// ErrBulkheadFull so they can fail fast or fall back instead of piling up.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// Cap concurrency at 3 — that's the number of in-flight calls the protected
	// resource (a connection pool, a fragile downstream) can safely sustain.
	policy := r8e.NewPolicy[string]("bulkhead-demo",
		r8e.WithBulkhead(3), // max 3 concurrent calls
	)

	// Launch 6 concurrent calls. Only 3 should succeed; the rest get
	// ErrBulkheadFull.
	fmt.Println("=== Launching 6 concurrent calls (bulkhead capacity: 3) ===")

	var wg sync.WaitGroup
	for i := 1; i <= 6; i++ {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			result, err := policy.Do(
				ctx,
				func(_ context.Context) (string, error) {
					// Hold the slot for 200ms so several workers overlap and the
					// bulkhead actually fills — instant work would never collide.
					time.Sleep(200 * time.Millisecond)
					return fmt.Sprintf("worker %d done", id), nil
				},
			)
			// A rejection isn't a downstream error — it's the bulkhead doing its
			// job, shedding load before the resource is overrun. Distinguish it
			// so the caller can fall back or retry rather than treat it as a real
			// failure.
			switch {
			case errors.Is(err, r8e.ErrBulkheadFull):
				fmt.Printf("  worker %d: REJECTED (bulkhead full)\n", id)
			case err != nil:
				fmt.Printf("  worker %d: error: %v\n", id, err)
			default:
				fmt.Printf("  worker %d: %s\n", id, result)
			}
		}(i)
		// Stagger launches by 10ms so the first 3 are still holding slots (each
		// busy for 200ms) when workers 4-6 arrive — that's what forces the
		// rejections. Fire them all at the exact same instant and the outcome
		// would hinge on raw scheduler luck.
		time.Sleep(10 * time.Millisecond)
	}

	wg.Wait()
}
