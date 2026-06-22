// Example 20-coalesce: Demonstrates request coalescing (singleflight).
//
// The problem it solves: when a hot cache key expires, N requests miss at once
// and all stampede the same slow backend — N identical calls for one piece of
// data. Coalescing collapses that burst into a single shared execution: the
// first caller (the leader) does the work, and everyone who arrives while it is
// in flight (the followers) waits for and shares that one result. This program
// fires 50 simultaneous callers at one key to show the dedup, then fires three
// distinct keys to show that only overlapping, same-key calls are merged.
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
// the call context so the policy's key function can read it back. Coalescing
// keys off the *context* rather than the arguments because the policy's Do only
// sees ctx and fn — so request identity has to be stamped into ctx upstream.
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
		// The 50ms sleep is what makes coalescing observable: it keeps the leader
		// in flight long enough for the other 49 callers to arrive and attach as
		// followers. With an instant backend they might not overlap at all, and
		// you'd see several "leaders" race through before any dedup kicked in.
		// We still honour ctx.Done() so a cancelled caller isn't left blocking.
		select {
		case <-time.After(50 * time.Millisecond):
		case <-ctx.Done():
			return "", ctx.Err()
		}

		return "profile of " + userKey(ctx), nil
	}

	// The whole burst targets the same user ("alice"), so it models the exact
	// moment a hot key expires and every in-flight request misses together. We
	// expect 50 callers to produce just one backend call.
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

	// Leaders vs followers makes the dedup concrete: one leader ran the work and
	// the rest attached as followers. Their ratio is the saved-call rate — every
	// follower is a backend call that didn't happen.
	m := policy.Metrics()
	fmt.Printf("  coalesce leaders:   %d\n", m.CoalesceLeaders)
	fmt.Printf("  coalesce followers: %d (downstream calls saved)\n", m.CoalesceFollowers)

	// The contrast case: coalescing keys on identity, so three *different* users
	// must NOT merge — we'd expect 3 backend calls here. We reset the counter so
	// this section's number stands on its own.
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

	// Coalescing only dedupes calls that overlap in time; once a leader finishes
	// its key is released. By now every group has drained, so the in-flight gauge
	// is back to zero — proof the merged work didn't leak past its callers.
	fmt.Printf("  3 distinct keys -> %d backend call(s)\n", backendCalls.Load())
	fmt.Printf("  coalesce in-flight now: %d\n", policy.Metrics().CoalesceInFlight)
}
