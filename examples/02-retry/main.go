// Example 02-retry: a tour of the retry pattern and its controls.
//
// Transient failures (a dropped connection, a momentary 503) usually clear on
// their own, so retrying turns a flaky dependency into a reliable one — but
// retrying blindly can hammer a struggling service or wait far too long. This
// example walks through every backoff strategy (constant, exponential, linear,
// exponential-with-jitter) and the controls that keep retries safe: MaxDelay to
// cap the wait, PerAttemptTimeout to bound each individual try, and the
// classification (Transient/Permanent) and RetryIf predicate that decide which
// errors are even worth retrying.
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

func main() {
	ctx := context.Background()

	// --- Constant backoff ---
	// The simplest strategy: wait a fixed delay between every attempt. Good when
	// the dependency recovers on a predictable cadence (e.g. polling for a job to
	// finish) and there is no benefit to backing off harder over time.
	fmt.Println("=== Constant Backoff ===")

	// We fail the first two attempts and succeed on the third, so you can watch
	// retry actually kick in. r8e.Transient marks the error as retriable; without
	// that classification the same default applies, but flagging it is explicit.
	attempt := 0
	policy := r8e.NewPolicy[string]("constant",
		r8e.WithRetry(3, r8e.ConstantBackoff(100*time.Millisecond)),
	)
	result, err := policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		if attempt < 3 {
			return "", r8e.Transient(errors.New("temporary failure"))
		}

		return "success on attempt 3", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Exponential backoff with MaxDelay ---
	// The distributed-systems default: each delay roughly doubles (50ms, 100ms,
	// 200ms, ...), so a brief blip is retried quickly while a longer outage backs
	// off fast and stops piling load onto the dependency. Doubling is unbounded,
	// though, so MaxDelay caps it at 500ms — otherwise late attempts could wait
	// many seconds for no good reason.
	fmt.Println("=== Exponential Backoff + MaxDelay ===")

	attempt = 0
	policy = r8e.NewPolicy[string]("exponential",
		r8e.WithRetry(5, r8e.ExponentialBackoff(50*time.Millisecond),
			r8e.MaxDelay(500*time.Millisecond),
		),
	)
	result, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		if attempt < 4 {
			return "", r8e.Transient(errors.New("still failing"))
		}

		return "success on attempt 4", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Linear backoff ---
	// A middle ground: the delay grows in fixed steps (100ms, 200ms, 300ms, ...)
	// rather than doubling. Less aggressive than exponential, so it is handy when
	// you want backoff to ramp up but stay gentle and predictable.
	fmt.Println("=== Linear Backoff ===")

	attempt = 0
	policy = r8e.NewPolicy[string]("linear",
		r8e.WithRetry(3, r8e.LinearBackoff(100*time.Millisecond)),
	)
	result, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		if attempt < 2 {
			return "", r8e.Transient(errors.New("one more try"))
		}

		return "success on attempt 2", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Exponential jitter backoff ---
	// Same exponential growth, but each delay is randomized within [0, base*2^n].
	// When many clients fail at the same instant (e.g. a shared dependency
	// hiccups), fixed delays make them all retry in lockstep and re-overload it —
	// the "thundering herd". Jitter spreads the retries out in time to avoid that.
	fmt.Println("=== Exponential Jitter Backoff ===")

	attempt = 0
	policy = r8e.NewPolicy[string]("jitter",
		r8e.WithRetry(4, r8e.ExponentialJitterBackoff(50*time.Millisecond)),
	)
	result, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		if attempt < 3 {
			return "", r8e.Transient(errors.New("jittery failure"))
		}

		return "success on attempt 3", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- Permanent error stops retries ---
	// Not every failure is worth retrying. A bad request or invalid input will
	// fail identically no matter how many times you try, so retrying just wastes
	// time and budget. Wrapping the error with r8e.Permanent tells the retry
	// engine to give up immediately: despite a budget of 5, only one attempt runs.
	fmt.Println("=== Permanent Error (stops retries) ===")

	attempt = 0
	policy = r8e.NewPolicy[string]("permanent",
		r8e.WithRetry(5, r8e.ConstantBackoff(50*time.Millisecond)),
	)
	_, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		return "", r8e.Permanent(errors.New("invalid input"))
	})
	fmt.Printf("  err: %v (only 1 attempt, stopped by Permanent)\n\n", err)

	// --- PerAttemptTimeout ---
	// A retry budget protects against failures, but not against a single attempt
	// hanging forever. PerAttemptTimeout bounds each individual try independently
	// of any global policy timeout: a slow attempt is cancelled via its context
	// and counts as a failure, freeing the next attempt to proceed. Below, the
	// first two attempts sleep past the 100ms deadline and get cancelled; the
	// third returns promptly and succeeds.
	fmt.Println("=== PerAttemptTimeout ===")

	attempt = 0
	policy = r8e.NewPolicy[string]("per-attempt-timeout",
		r8e.WithRetry(3, r8e.ConstantBackoff(50*time.Millisecond),
			r8e.PerAttemptTimeout(100*time.Millisecond),
		),
	)
	result, err = policy.Do(ctx, func(ctx context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		if attempt < 3 {
			// Simulate a slow call (200ms) that overshoots the 100ms per-attempt
			// timeout. Honoring ctx.Done() is what lets the deadline actually cut
			// the work short — a function that ignored its context would block the
			// full 200ms regardless of the timeout.
			select {
			case <-time.After(200 * time.Millisecond):
				return "too slow", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}

		return "fast response", nil
	})
	fmt.Printf("  result: %q, err: %v\n\n", result, err)

	// --- RetryIf predicate ---
	// Sometimes you can't (or don't want to) wrap errors as Permanent at the
	// source — the classification lives with the caller. RetryIf is a predicate
	// that inspects each error and decides whether it is worth another try, giving
	// you per-policy control over what counts as retriable. Here "not found" is a
	// definitive answer, so we refuse to retry it.
	fmt.Println("=== RetryIf Predicate ===")

	errNotFound := errors.New("not found")
	attempt = 0
	policy = r8e.NewPolicy[string]("retry-if",
		r8e.WithRetry(5, r8e.ConstantBackoff(50*time.Millisecond),
			r8e.RetryIf(func(err error) bool {
				// errors.Is unwraps, so this still matches even though the call
				// site wraps errNotFound with %w. Returning false stops retries.
				return !errors.Is(err, errNotFound)
			}),
		),
	)
	// The function always returns a wrapped "not found"; RetryIf rejects it on the
	// very first attempt, so the budget of 5 is never spent.
	_, err = policy.Do(ctx, func(_ context.Context) (string, error) {
		attempt++
		fmt.Printf("  attempt %d\n", attempt)

		return "", fmt.Errorf("wrap: %w", errNotFound)
	})
	fmt.Printf("  err: %v (stopped by RetryIf on attempt 1)\n", err)
}
