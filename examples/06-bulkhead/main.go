// Example 06-bulkhead: Demonstrates concurrency limiting with the bulkhead
// pattern.
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
					// Simulate work.
					time.Sleep(200 * time.Millisecond)
					return fmt.Sprintf("worker %d done", id), nil
				},
			)
			switch {
			case errors.Is(err, r8e.ErrBulkheadFull):
				fmt.Printf("  worker %d: REJECTED (bulkhead full)\n", id)
			case err != nil:
				fmt.Printf("  worker %d: error: %v\n", id, err)
			default:
				fmt.Printf("  worker %d: %s\n", id, result)
			}
		}(i)
		// Small stagger so calls arrive while previous are still running.
		time.Sleep(10 * time.Millisecond)
	}

	wg.Wait()
}
