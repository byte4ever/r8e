// Example 53-shared-bulkhead: Demonstrates WithSharedBulkhead, which lets
// several policies share one bulkhead and so one cap on the calls in flight.
//
// The problem it solves: a downstream API accepts a fixed number of concurrent
// calls, whatever route they use. Each route has its own policy, and a
// WithBulkhead per policy caps each route on its own: two routes with a
// bulkhead of 2 each can still send 4 calls at once.
//
// WithSharedBulkhead attaches one Bulkhead, built with NewBulkhead, to every
// policy. At most its capacity of calls are in flight across all of them. A
// call on one route can then be refused because of the calls on another route.
// Each policy reports its own calls in its hooks and counters, while the gauges
// describe the same bulkhead in every policy.
//
// The demo shares a bulkhead of 2 between an "orders" and a "users" policy.
// Two orders calls hold the two slots, a users call is refused, then the
// orders calls end and the users call goes through.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/byte4ever/r8e"
)

func main() {
	ctx := context.Background()

	// One bulkhead for the whole API. Its own hooks stay empty: a shared
	// bulkhead sends its events to the hooks of the policy that makes the
	// call.
	api := r8e.NewBulkhead(2, r8e.RealClock{}, &r8e.Hooks{})

	usersFull := 0
	orders := r8e.NewPolicy[string]("orders", r8e.WithSharedBulkhead(api))
	users := r8e.NewPolicy[string]("users",
		r8e.WithSharedBulkhead(api),
		r8e.WithHooks(&r8e.Hooks{OnBulkheadFull: func() { usersFull++ }}),
	)

	fmt.Println("=== Two orders calls hold both slots ===")

	release, done := holdSlots(ctx, orders, 2)

	fmt.Printf("  orders: in use=%d/%d\n", orders.Metrics().BulkheadInUse, orders.Metrics().BulkheadCap)
	fmt.Printf("  users:  in use=%d/%d (the same bulkhead)\n",
		users.Metrics().BulkheadInUse, users.Metrics().BulkheadCap)

	fmt.Println("\n=== A users call while orders holds the slots ===")

	userCall(ctx, users)
	fmt.Printf("  users:  rejected=%d OnBulkheadFull=%d\n", users.Metrics().BulkheadRejected, usersFull)
	fmt.Printf("  orders: rejected=%d\n", orders.Metrics().BulkheadRejected)

	fmt.Println("\n=== The orders calls end ===")

	close(release)
	done.Wait()

	fmt.Printf("  in use=%d/%d\n", users.Metrics().BulkheadInUse, users.Metrics().BulkheadCap)
	userCall(ctx, users)
}

// holdSlots starts the given number of calls through policy, each of which
// stays inside the bulkhead until release is closed. It returns once all of them hold a slot, so the
// state that follows does not depend on goroutine timing. done ends when every
// call has returned and freed its slot.
func holdSlots(ctx context.Context, policy *r8e.Policy[string], calls int) (chan struct{}, *sync.WaitGroup) {
	release := make(chan struct{})
	inside := make(chan struct{}, calls)

	var done sync.WaitGroup

	for range calls {
		done.Go(func() {
			_, err := policy.Do(ctx, func(context.Context) (string, error) {
				inside <- struct{}{}

				<-release

				return "order", nil
			})
			if err != nil {
				fmt.Printf("  orders call failed: %v\n", err)
			}
		})
	}

	for range calls {
		<-inside
	}

	return release, &done
}

// userCall makes one call through the users policy and prints its outcome.
func userCall(ctx context.Context, users *r8e.Policy[string]) {
	ran := false

	result, err := users.Do(ctx, func(context.Context) (string, error) {
		ran = true

		return "user 42", nil
	})

	fmt.Printf("  users call: result=%q err=%v bulkhead full=%v fn ran=%v\n",
		result, err, errors.Is(err, r8e.ErrBulkheadFull), ran)
}
