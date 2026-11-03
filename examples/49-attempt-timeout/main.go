// Example 49-attempt-timeout: Demonstrates PerAttemptTimeout and how to tell
// its cut apart from the caller's own deadline.
//
// The problem it solves: a slow attempt should not use up the whole time a
// caller has. PerAttemptTimeout gives each retry attempt its own deadline, so
// a stuck attempt is cut and the next one can run. Both this cut and the
// caller's deadline end the attempt with context.DeadlineExceeded, so that
// error alone does not say who ended it. A log line, an alert or a retry
// decision often needs to know.
//
// When the per-attempt deadline cuts a failed attempt, r8e wraps the attempt's
// error with ErrAttemptTimeout, fires Hooks.OnAttemptTimeout with the attempt
// number, and counts the cut in Metrics().AttemptTimeouts. An attempt ended by
// the caller's deadline, a time budget or WithTimeout carries no
// ErrAttemptTimeout.
//
// The demo runs one policy against a slow downstream in three phases: two
// attempts cut then a fast one, every attempt cut, and a caller whose deadline
// ends the attempt first.
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

// downstream hangs on its first slowAttempts attempts, until the attempt's
// context ends, then answers at once.
type downstream struct {
	slowAttempts int
	attempts     int
}

const attemptTimeout = 50 * time.Millisecond

func main() {
	ctx := context.Background()

	policy := r8e.NewPolicy[string]("attempt-timeout-demo",
		r8e.WithRetry(3, r8e.ConstantBackoff(5*time.Millisecond),
			r8e.PerAttemptTimeout(attemptTimeout),
		),
		r8e.WithHooks(&r8e.Hooks{
			OnAttemptTimeout: func(attempt int) {
				fmt.Printf("  [hook] attempt %d cut by PerAttemptTimeout\n", attempt)
			},
		}),
	)

	// Phase 1: the first two attempts hang and are cut after 50ms each; the
	// third answers at once. The caller gets a result in about 110ms instead of
	// waiting on a stuck call.
	fmt.Println("=== Phase 1: slow attempts are cut, the next one succeeds ===")

	recovering := &downstream{slowAttempts: 2}
	result, err := policy.Do(ctx, recovering.call)
	fmt.Printf("  result=%q err=%v\n", result, err)
	fmt.Printf("  AttemptTimeouts=%d\n", policy.Metrics().AttemptTimeouts)

	// Phase 2: every attempt hangs. The error carries ErrAttemptTimeout, and
	// the downstream's own error stays in the chain.
	fmt.Println("\n=== Phase 2: every attempt is cut ===")

	stuck := &downstream{slowAttempts: 3}
	_, err = policy.Do(ctx, stuck.call)
	describe(err)
	fmt.Printf("  AttemptTimeouts=%d\n", policy.Metrics().AttemptTimeouts)

	// Phase 3: the caller allows 30ms, less than the 50ms attempt deadline.
	// The caller's deadline ends the attempt, so the error is the same
	// context.DeadlineExceeded, but without ErrAttemptTimeout and without a cut
	// counted.
	fmt.Println("\n=== Phase 3: the caller's own deadline ends the attempt ===")

	callerCtx, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	_, err = policy.Do(callerCtx, (&downstream{slowAttempts: 3}).call)

	cancel()
	describe(err)
	fmt.Printf("  AttemptTimeouts=%d\n", policy.Metrics().AttemptTimeouts)
}

// describe prints the error and the checks a caller uses to tell who ended
// the call.
func describe(err error) {
	fmt.Printf("  err=%q\n", err)
	fmt.Printf("  errors.Is(err, r8e.ErrAttemptTimeout)=%v\n", errors.Is(err, r8e.ErrAttemptTimeout))
	fmt.Printf("  errors.Is(err, context.DeadlineExceeded)=%v\n", errors.Is(err, context.DeadlineExceeded))
	fmt.Printf("  r8e.ErrorCode(err)=%q\n", r8e.ErrorCode(err))
}

func (d *downstream) call(ctx context.Context) (string, error) {
	d.attempts++

	if d.attempts > d.slowAttempts {
		return fmt.Sprintf("answer on attempt %d", d.attempts), nil
	}

	<-ctx.Done()

	return "", fmt.Errorf("read response: %w", ctx.Err())
}
