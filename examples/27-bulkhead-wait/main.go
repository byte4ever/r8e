// Example 27-bulkhead-wait: Demonstrates the bounded FIFO wait. Unlike the plain
// bulkhead (example 06), which rejects immediately when full, this one lets a
// short burst queue for a bounded time: callers that get a slot within the
// max-wait succeed, callers that wait too long give up with ErrBulkheadTimeout,
// and callers arriving once the queue is full are rejected with ErrBulkheadFull.
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

	policy := r8e.NewPolicy[string]("bulkhead-wait-demo",
		// 1 slot; up to 2 callers may queue for 150ms before giving up.
		r8e.WithBulkhead(1,
			r8e.BulkheadMaxWait(150*time.Millisecond),
			r8e.BulkheadQueueDepth(2),
		),
		r8e.WithHooks(&r8e.Hooks{
			OnBulkheadQueued:  func() { fmt.Println("  [hook] caller queued for a slot") },
			OnBulkheadTimeout: func() { fmt.Println("  [hook] caller gave up after max-wait") },
		}),
	)

	// Each call holds the single slot for 100ms.
	work := func(id int) (string, error) {
		return policy.Do(ctx, func(_ context.Context) (string, error) {
			time.Sleep(100 * time.Millisecond)

			return fmt.Sprintf("worker %d served", id), nil
		})
	}

	fmt.Println("=== 1 slot, queue depth 2, max-wait 150ms — 5 callers ===")

	var wg sync.WaitGroup

	for i := 1; i <= 5; i++ {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			result, err := work(id)
			switch {
			case errors.Is(err, r8e.ErrBulkheadTimeout):
				fmt.Printf("  worker %d: TIMED OUT waiting\n", id)
			case errors.Is(err, r8e.ErrBulkheadFull):
				fmt.Printf("  worker %d: REJECTED (queue full)\n", id)
			case err != nil:
				fmt.Printf("  worker %d: error: %v\n", id, err)
			default:
				fmt.Printf("  worker %d: %s\n", id, result)
			}
		}(i)
		// Stagger arrivals so the queue and the wait come into play.
		time.Sleep(15 * time.Millisecond)
	}

	wg.Wait()
}
