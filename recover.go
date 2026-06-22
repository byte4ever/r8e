package r8e

import (
	"context"
	"fmt"
	"runtime/debug"
)

// Pattern: Recover — catches panics from the user function and converts them
// to errors, letting the rest of the resilience chain (retry, fallback, circuit
// breaker) handle them instead of crashing the process.

// PanicError wraps a value recovered from a panic, so callers can both match
// it with errors.Is(err, [ErrPanic]) and inspect the original value and
// goroutine stack trace via errors.As.
type PanicError struct {
	// Value is the value passed to panic().
	Value any
	// Stack is the goroutine stack trace captured at the time of recovery.
	Stack []byte
}

// Error implements the error interface.
func (e *PanicError) Error() string {
	return fmt.Sprintf("recovered panic: %v", e.Value)
}

// Is reports true for [ErrPanic], enabling errors.Is matching while
// preserving the ability to obtain the original Value and Stack via errors.As.
func (*PanicError) Is(target error) bool {
	return target == ErrPanic
}

// DoRecover executes fn and catches any panic, returning it as a *[PanicError]
// instead of propagating the panic up the call stack. The [Hooks.OnPanic] hook
// fires and the [PolicyMetrics.PanicsRecovered] counter increments for each
// caught panic. A non-nil hooks is assumed (the policy wires a non-nil
// instrumented Hooks; external callers should pass a non-nil *Hooks or a
// zero-value &Hooks{}).
//
// Because recovery sits innermost in the chain, the retry pattern (if
// configured) sees the recovered error and can retry the panicking call.
// Use [WithRecover] (via [NewPolicy]) rather than calling DoRecover directly
// to guarantee correct priority placement in the middleware chain.
//
//nolint:ireturn // generic type parameter T, not an interface
func DoRecover[T any](
	ctx context.Context,
	fn func(context.Context) (T, error),
	hooks *Hooks,
) (val T, err error) {
	defer func() {
		if rv := recover(); rv != nil {
			err = &PanicError{Value: rv, Stack: debug.Stack()}

			// Guard against a panicking OnPanic hook: recover() was already
			// consumed above, so a second panic here would escape uncaught.
			func() {
				defer func() { recover() }() //nolint:errcheck // swallow hook panics; original error already set

				hooks.emitPanic(rv)
			}()
		}
	}()

	return fn(ctx) //nolint:wrapcheck // caller's error returned as-is
}

func newRecoverEntry[T any](hooks *Hooks) PatternEntry[T] {
	return PatternEntry[T]{
		Priority: priorityRecover,
		Name:     "recover",
		MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
			return func(ctx context.Context) (T, error) {
				return DoRecover[T](ctx, next, hooks)
			}
		},
	}
}
