// Example 41-codel-queue: Demonstrates the controlled-delay (CoDel) queue
// discipline on a bulkhead.
//
// The problem it solves: a plain bounded wait (example 27) sheds on a FIXED
// per-caller deadline and serves strictly FIFO. Under sustained overload that is
// the worst of both worlds — the oldest callers (whose clients have probably
// already given up) are served first, and every caller waits its full deadline
// before being shed, so the queue stays full of doomed work. BulkheadCoDel
// instead watches the standing queue delay: once the oldest caller has been
// waiting above the target for a full interval the queue is declared overloaded,
// and from then on it (1) sheds callers that have already waited past the slough
// timeout with ErrCoDelShed, and (2) serves the NEWEST caller first (adaptive
// LIFO) so the freshest, likeliest-still-wanted work keeps moving. When the
// backlog clears it returns to plain FIFO with no shedding.
//
// This demo fires a burst of callers at a one-slot bulkhead with a slow worker,
// so the queue backs up, CoDel latches overloaded, the stale callers are shed,
// and the rest are served newest-first.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	var (
		queued atomic.Int64
		shed   atomic.Int64
	)

	policy := r8e.NewPolicy[string]("codel-demo",
		// One slot with the controlled-delay discipline. The folly defaults are
		// target 5ms / interval 100ms; here they are scaled down (target 3ms,
		// interval 12ms) so the queue tips into "overloaded" within a sub-second
		// demo. A generous queue depth lets the whole stream wait rather than be
		// rejected on arrival.
		r8e.WithBulkhead(1,
			r8e.BulkheadCoDel(3*time.Millisecond, 12*time.Millisecond),
			r8e.BulkheadQueueDepth(32),
		),
		// The hooks make the discipline visible: one line when a caller joins the
		// queue, another when CoDel sheds a stale caller under overload.
		r8e.WithHooks(&r8e.Hooks{
			OnBulkheadQueued: func() { queued.Add(1) },
			OnCoDelShed:      func() { shed.Add(1) },
		}),
	)

	// Each call holds the single slot for 20ms, so a steady stream of arrivals
	// must queue behind the one running worker — and the queue's standing delay
	// quickly climbs past the 3ms target.
	work := func(id int) (string, error) {
		return policy.Do(ctx, func(_ context.Context) (string, error) {
			time.Sleep(20 * time.Millisecond)

			return fmt.Sprintf("caller %2d served", id), nil
		})
	}

	const callers = 20

	fmt.Printf("=== 1 slot, CoDel(3ms, 12ms), %d callers, ~5ms apart ===\n", callers)

	var (
		wg   sync.WaitGroup
		errs = make([]error, callers)
	)

	for i := range callers {
		wg.Add(1)

		// Each goroutine records only its outcome error; formatting and counting
		// happen after the join so each switch case stays a single statement.
		go func(id int) {
			defer wg.Done()

			_, errs[id] = work(id)
		}(i)

		// Stagger arrivals so the queue builds up an age gradient (old at the
		// front, fresh at the back) and fresh callers keep arriving once the queue
		// is overloaded — the condition under which adaptive LIFO is visible.
		time.Sleep(5 * time.Millisecond)
	}

	wg.Wait()

	served := 0

	for _, err := range errs {
		if err == nil {
			served++
		}
	}

	for id, err := range errs {
		switch {
		case err == nil:
			fmt.Printf("  caller %2d: served\n", id)
		case errors.Is(err, r8e.ErrCoDelShed):
			fmt.Printf("  caller %2d: shed by controlled-delay queue\n", id)
		default:
			fmt.Printf("  caller %2d: %v\n", id, err)
		}
	}

	fmt.Println()
	fmt.Printf("queued: %d, served: %d, shed (ErrCoDelShed): %d\n",
		queued.Load(), served, shed.Load())

	m := policy.Metrics()
	fmt.Printf("metrics: CoDelShed=%d, BulkheadQueued=%d, CoDelLoad=%.2f\n",
		m.CoDelShed, m.BulkheadQueued, m.CoDelLoad)

	fmt.Println("\nUnder overload the stale front callers are shed and the freshest " +
		"are served first (adaptive LIFO); a healthy queue stays FIFO with no shedding.")
}
