// Example 44-circuit-breaker-classifier: Demonstrates how a classifier tells the
// circuit breaker which calls say something about the downstream's health.
//
// The problem it solves: by default the breaker counts every non-nil error as a
// failure and every nil error as a success. Three kinds of call break that
// rule. A business answer such as "order not found" is an error for the caller,
// yet it proves the downstream is up. A call whose caller gave up (a deadline, a
// shutdown) proves nothing either way. A call the policy's own bulkhead turned
// away never reached the downstream at all. Counting the first or the third
// opens the breaker on a healthy service; counting the second as a failure
// reopens a recovering breaker, and reporting it as nil resets the failure
// counter so a hanging downstream never trips the breaker.
//
// CircuitBreakerClassifier maps each call to OutcomeSuccess, OutcomeFailure or
// OutcomeNeutral. A neutral call leaves the breaker untouched, while the caller
// still receives the real error. The policy's own timeout is never a caller
// cancellation, so a hanging downstream still opens the breaker.
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

// errNotFound is the business answer the downstream gives for an unknown order.
var errNotFound = errors.New("order not found")

func main() {
	ctx := context.Background()

	// WithTimeout returns to the caller as soon as its deadline passes, and the
	// breaker records the call when the downstream function returns, a moment
	// later. The hook signals this channel, so phase 4 can wait for the
	// breaker to open instead of guessing how long that takes.
	opened := make(chan struct{}, 1)

	policy := r8e.NewPolicy[string]("classifier-demo",
		// The policy's own deadline. It is longer than the callers' deadlines in
		// phase 2, so those callers give up first.
		r8e.WithTimeout(300*time.Millisecond),
		r8e.WithCircuitBreaker(
			// Two failures in a row open the breaker, so any miscounted call would
			// show up fast.
			r8e.FailureThreshold(2),
			r8e.RecoveryTimeout(time.Second),
			r8e.CircuitBreakerClassifier(classify),
		),
		r8e.WithHooks(&r8e.Hooks{
			OnCircuitOpen: func() {
				fmt.Println("  [hook] circuit OPENED")

				select {
				case opened <- struct{}{}:
				default: // a signal is already waiting
				}
			},
		}),
	)

	// Phase 1: the downstream answers "not found". Without the classifier, the
	// second answer would open the breaker.
	fmt.Println(`=== Phase 1: "not found" answers do not count ===`)

	for i := 1; i <= 3; i++ {
		_, err := policy.Do(ctx, func(context.Context) (string, error) {
			return "", errNotFound
		})
		report(policy, i, err)
	}

	// Phase 2: the downstream is slow and each caller gives up after 50ms. The
	// calls end with the caller's own context error, which the classifier
	// treats as neutral.
	fmt.Println("\n=== Phase 2: callers that give up do not count ===")

	for i := 1; i <= 2; i++ {
		callerCtx, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
		_, err := policy.Do(callerCtx, slowDownstream)

		cancel()
		report(policy, i, err)
	}

	// Phase 3: one call holds the bulkhead's only slot while two more arrive.
	// The bulkhead turns them away; the classifier makes those rejections
	// neutral, so they do not open the breaker.
	fmt.Println("\n=== Phase 3: a burst past the bulkhead does not count ===")
	burstPastBulkhead(ctx)

	// Phase 4: the downstream hangs. The policy's timeout cuts each call, and
	// those calls count as failures even with the classifier, so the second one
	// opens the breaker and the third call fails fast.
	fmt.Println("\n=== Phase 4: a hanging downstream still opens the breaker ===")

	for i := 1; i <= 3; i++ {
		_, err := policy.Do(ctx, hangingDownstream)

		if i == 2 {
			waitOpened(opened)
		}

		report(policy, i, err)
	}
}

// burstPastBulkhead runs two calls while a third holds the only slot of a
// bulkhead, then lets the held call finish. It uses a policy of its own: in the
// main policy, WithTimeout returns to the caller before the call it abandons
// frees its slot, so a bulkhead there would also turn away the calls of the
// other phases.
func burstPastBulkhead(ctx context.Context) {
	policy := r8e.NewPolicy[string]("classifier-demo-bulkhead",
		// One slot, so a second concurrent call is turned away at once. The
		// bulkhead runs inside the breaker, which sees its rejections.
		r8e.WithBulkhead(1),
		r8e.WithCircuitBreaker(
			r8e.FailureThreshold(2),
			r8e.CircuitBreakerClassifier(classify),
		),
	)

	holding := make(chan struct{})
	release := make(chan struct{})
	held := make(chan string, 1)

	go func() {
		val, err := policy.Do(ctx, func(ctx context.Context) (string, error) {
			close(holding)

			select {
			case <-release:
				return "order 7", nil
			case <-ctx.Done():
				return "", fmt.Errorf("wait for order: %w", ctx.Err())
			}
		})
		if err != nil {
			val = err.Error()
		}

		held <- val
	}()

	<-holding

	for i := 1; i <= 2; i++ {
		_, err := policy.Do(ctx, func(context.Context) (string, error) {
			return "unreached", nil
		})
		report(policy, i, err)
	}

	close(release)
	fmt.Printf("  the call holding the slot: %s (state=%s)\n", <-held, policy.Metrics().CircuitState)
}

// waitOpened waits until the OnCircuitOpen hook has fired, or gives up after a
// second so a broken setup cannot hang the example.
func waitOpened(opened <-chan struct{}) {
	select {
	case <-opened:
	case <-time.After(time.Second):
		fmt.Println("  the breaker did not open within a second")
	}
}

// classify makes neutral a business "not found" and a rejection by the
// rate limiter, the bulkhead or the adaptive concurrency limiter, as
// IsLocalRejection reports it, and delegates everything else to
// CallerCancellationIsNeutral: a caller cancellation is neutral, any other
// error is a failure, and nil is a success.
func classify(ctx context.Context, err error) r8e.Outcome {
	if errors.Is(err, errNotFound) || r8e.IsLocalRejection(err) {
		return r8e.OutcomeNeutral
	}

	return r8e.CallerCancellationIsNeutral(ctx, err)
}

// slowDownstream answers after 200ms unless its caller gives up first.
func slowDownstream(ctx context.Context) (string, error) {
	select {
	case <-time.After(200 * time.Millisecond):
		return "order 42", nil
	case <-ctx.Done():
		return "", fmt.Errorf("wait for order: %w", ctx.Err())
	}
}

// hangingDownstream never answers; it only returns once its context ends.
func hangingDownstream(ctx context.Context) (string, error) {
	<-ctx.Done()

	return "", fmt.Errorf("wait for order: %w", ctx.Err())
}

func report(policy *r8e.Policy[string], call int, err error) {
	fmt.Printf("  call %d: %v (state=%s)\n", call, err, policy.Metrics().CircuitState)
}
