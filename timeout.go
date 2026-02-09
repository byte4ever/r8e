package r8e

import (
	"context"
	"time"
)

// Pattern: Timeout — wraps a call with a context deadline, returning
// ErrTimeout if the operation does not complete in time. Distinguishes
// between timeout-caused cancellation and parent context cancellation.

// DoTimeout executes fn with a timeout. If fn does not complete within d,
// the context is cancelled and ErrTimeout is returned.
//
//nolint:ireturn // generic type parameter T, not an interface
func DoTimeout[T any](
	ctx context.Context,
	timeout time.Duration,
	fn func(context.Context) (T, error),
	hooks *Hooks,
) (T, error) {
	var zero T

	// If the parent context is already done, return its error immediately.
	if ctx.Err() != nil {
		return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
	}

	// Create derived context with timeout.
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Run fn in a goroutine and collect result via channel.
	type result struct {
		val T
		err error
	}

	ch := make(chan result, 1)

	go func() {
		v, err := fn(timeoutCtx)
		ch <- result{val: v, err: err}
	}()

	// Wait for fn to complete or context to expire.
	select {
	case r := <-ch:
		return r.val, r.err
	case <-timeoutCtx.Done():
		// Distinguish between timeout and parent cancellation.
		// If the parent context is done, the parent was cancelled externally.
		if ctx.Err() != nil {
			return zero, ctx.Err() //nolint:wrapcheck // preserving context error identity
		}
		// Otherwise, the derived context's deadline was exceeded.
		hooks.emitTimeout()

		return zero, ErrTimeout
	}
}
