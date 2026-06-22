// Example 27-bulkhead-wait: Demonstrates the bounded FIFO wait on a bulkhead.
//
// The problem it solves: a plain bulkhead (example 06) rejects immediately the
// moment it is full, which throws away callers that a slot would have freed for
// a few milliseconds later — turning a brief, survivable burst into a wall of
// errors. BulkheadMaxWait lets a short burst queue in FIFO order for a bounded
// time instead, absorbing the spike without unbounded blocking. The three
// outcomes show the full contract: callers that get a slot within the max-wait
// succeed, callers that wait too long give up with ErrBulkheadTimeout, and
// callers arriving once the bounded queue is full are rejected immediately with
// ErrBulkheadFull.
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
		// Deliberately tiny limits so all three outcomes appear with just five
		// callers: 1 slot, a queue depth of 2 (so at most 2 callers wait), and a
		// 150ms max-wait that is longer than one 100ms job but shorter than two —
		// the head of the queue is served, the tail times out.
		r8e.WithBulkhead(1,
			r8e.BulkheadMaxWait(150*time.Millisecond),
			r8e.BulkheadQueueDepth(2),
		),
		// The hooks make the queueing visible: one line when a caller starts
		// waiting, another when a waiter exhausts its max-wait and gives up.
		r8e.WithHooks(&r8e.Hooks{
			OnBulkheadQueued:  func() { fmt.Println("  [hook] caller queued for a slot") },
			OnBulkheadTimeout: func() { fmt.Println("  [hook] caller gave up after max-wait") },
		}),
	)

	// Each call holds the single slot for 100ms, so while one worker runs the
	// next arrivals have to queue (or be rejected) rather than proceed.
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

		// Each caller runs in its own goroutine because they contend for the
		// single slot concurrently — the bounded wait only has meaning when
		// callers overlap in time.
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
		// Stagger arrivals by 15ms so the slot, the bounded queue, and the
		// max-wait all come into play in sequence rather than all five callers
		// racing the gate at once.
		time.Sleep(15 * time.Millisecond)
	}

	wg.Wait()
}
