// Example 51-base-context: Demonstrates WithBaseContext, which lets the owner
// of a policy stop the calls the policy runs detached from their callers.
//
// The problem it solves: WithCoalesce runs the shared call under a context
// detached from every caller, so that one caller leaving cannot abort work the
// other callers wait for. The refresh-ahead reload of WithCache is detached the
// same way. Nothing outside the policy can then stop such a call: it runs
// until fn returns or WithTimeout cuts it, even after the client that owns the
// policy has shut down.
//
// WithBaseContext(ctx) ties those detached calls to a context the owner
// controls. Cancelling ctx stops them, and their context ends with ctx's
// cause. They keep the values of the caller's context and still ignore the
// caller's own cancellation. Once ctx has ended, a call with a coalescing key
// that misses the cache fails at once with context.Canceled, while a call with
// no coalescing key still runs on its caller's context.
//
// The demo starts a coalesced call, lets its caller leave, shuts the client
// down, then makes two more calls: one with a coalescing key, one without.
//
//nolint:forbidigo // This is an example program.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/byte4ever/r8e"
)

type (
	// userKey is the context key carrying the user a call is for.
	userKey struct{}

	// requestIDKey is the context key carrying the request ID of a call.
	requestIDKey struct{}
)

// errClientClosed is the cause the client gives when it shuts down.
var errClientClosed = errors.New("client closed")

func main() {
	// The client owns this context and cancels it when it shuts down.
	base, stop := context.WithCancelCause(context.Background())
	defer stop(nil)

	policy := r8e.NewPolicy[string]("",
		r8e.WithBaseContext(base),
		r8e.WithCoalesce(coalesceKey),
		// Coalescing requires a timeout. It is long on purpose: here the
		// base context, not the timeout, stops the shared call.
		r8e.WithTimeout(time.Minute),
	)

	fmt.Println("=== A coalesced call outlives its caller ===")

	causes := leaveSharedCall(policy)

	fmt.Printf("  shared calls still running: %d\n", policy.Metrics().CoalesceInFlight)

	fmt.Println("\n=== The client shuts down ===")

	// Cancelling the base context reaches the shared call, which no caller
	// can stop any more.
	stop(errClientClosed)

	fmt.Printf("  shared call stopped, cause: %v\n", <-causes)

	waitNoSharedCall(policy)

	fmt.Printf("  shared calls still running: %d\n", policy.Metrics().CoalesceInFlight)

	fmt.Println("\n=== Calls after the shutdown ===")

	callAfterShutdown(policy)
}

// leaveSharedCall starts a coalesced call whose fn waits for its context to
// end, then cancels the caller. It returns the channel on which fn reports
// the cause of its context's end.
func leaveSharedCall(policy *r8e.Policy[string]) <-chan error {
	started := make(chan struct{})
	causes := make(chan error, 1)

	ctx := context.WithValue(context.Background(), userKey{}, "user-42")
	ctx = context.WithValue(ctx, requestIDKey{}, "req-1")
	ctx, cancel := context.WithCancel(ctx)

	go func() {
		// The caller gives up once the shared call has started.
		<-started
		cancel()
	}()

	_, err := policy.Do(ctx, func(ctx context.Context) (string, error) {
		// The shared call keeps the caller's values.
		fmt.Printf("  shared call started, request ID: %v\n", ctx.Value(requestIDKey{}))
		close(started)

		// It ignores the caller's cancellation and waits for its own
		// context to end.
		<-ctx.Done()

		causes <- context.Cause(ctx)

		return "", ctx.Err()
	})

	fmt.Printf("  caller left: %v\n", err)

	return causes
}

// waitNoSharedCall waits until the policy has no shared call in flight. The
// shared call removes its key right after fn returns, on its own goroutine.
func waitNoSharedCall(policy *r8e.Policy[string]) {
	for policy.Metrics().CoalesceInFlight > 0 {
		time.Sleep(time.Millisecond)
	}
}

// callAfterShutdown makes one call with a coalescing key and one without,
// after the base context has ended, and counts how many times fn runs.
func callAfterShutdown(policy *r8e.Policy[string]) {
	var runs int

	fetch := func(context.Context) (string, error) {
		runs++

		return "fresh answer", nil
	}

	// A call with a key would run detached, and the base context has ended:
	// it fails at once, before fn runs.
	keyed := context.WithValue(context.Background(), userKey{}, "user-42")
	result, err := policy.Do(keyed, fetch)
	fmt.Printf("  with a key:    result=%q err=%v canceled=%v fn runs=%d\n",
		result, err, errors.Is(err, context.Canceled), runs)

	// A call with no key is not coalesced: it runs on its caller's context,
	// which is still live.
	result, err = policy.Do(context.Background(), fetch)
	fmt.Printf("  without a key: result=%q err=%v fn runs=%d\n", result, err, runs)
}

// coalesceKey returns the user the call is for, or "" to opt the call out of
// coalescing.
func coalesceKey(ctx context.Context) string {
	user, ok := ctx.Value(userKey{}).(string)
	if !ok {
		return ""
	}

	return user
}
