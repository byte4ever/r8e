// Example 20-coalesce: Demonstrates request coalescing (singleflight). A burst of
// concurrent requests for the same key collapses into one shared downstream call
// — the classic cache-stampede fix — while distinct keys run independently.
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

// ctxKey carries the coalescing key (here, the user id being fetched) through
// the call context so the policy's key function can read it back.
type ctxKey struct{}

func withUser(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

func userKey(ctx context.Context) string {
	if id, ok := ctx.Value(ctxKey{}).(string); ok {
		return id
	}

	return ""
}

func main() {
	// backendCalls counts how often the (slow) downstream actually runs, so the
	// deduplication is visible.
	var backendCalls atomic.Int64

	policy := r8e.NewPolicy[string]("user-fetch",
		// Coalescing requires a timeout: the shared call runs under a context
		// detached from its callers, and the timeout bounds it (NewPolicy panics
		// with ErrCoalesceWithoutTimeout otherwise).
		r8e.WithTimeout(time.Second),
		r8e.WithCoalesce(userKey),
	)

	fetch := func(ctx context.Context) (string, error) {
		backendCalls.Add(1)
		// Simulate a slow lookup so the concurrent callers overlap.
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}

		return "profile of " + userKey(ctx), nil
	}

	// --- 50 concurrent requests for the SAME user collapse into one call ---
	fmt.Println("=== Stampede on one hot key ===")

	const callers = 50

	ctx := withUser(context.Background(), "alice")

	var wg sync.WaitGroup

	wg.Add(callers)

	for range callers {
		go func() {
			defer wg.Done()

			if _, err := policy.Do(ctx, fetch); err != nil {
				fmt.Printf("  unexpected error: %v\n", err)
			}
		}()
	}

	wg.Wait()

	fmt.Printf("  %d concurrent callers -> %d backend call(s)\n",
		callers, backendCalls.Load())

	m := policy.Metrics()
	fmt.Printf("  coalesce leaders:   %d\n", m.CoalesceLeaders)
	fmt.Printf("  coalesce followers: %d (downstream calls saved)\n", m.CoalesceFollowers)

	// --- Distinct keys are not coalesced: each runs on its own ---
	fmt.Println("\n=== Distinct keys run independently ===")

	backendCalls.Store(0)

	wg.Add(3)

	for _, id := range []string{"bob", "carol", "dave"} {
		go func() {
			defer wg.Done()

			if _, err := policy.Do(withUser(context.Background(), id), fetch); err != nil {
				fmt.Printf("  unexpected error: %v\n", err)
			}
		}()
	}

	wg.Wait()

	fmt.Printf("  3 distinct keys -> %d backend call(s)\n", backendCalls.Load())
	fmt.Printf("  coalesce in-flight now: %d\n", policy.Metrics().CoalesceInFlight)
}
