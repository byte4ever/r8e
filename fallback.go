package r8e

import "context"

// Pattern: Fallback — catches a final error and either returns a static value
// or delegates to a fallback function, providing a last line of defence.

// DoFallback executes fn. On error, returns the fallback value instead.
//
//nolint:ireturn,unparam // generic type parameter T; error is always nil by
// design.
func DoFallback[T any](
	ctx context.Context,
	fn func(context.Context) (T, error),
	fallbackVal T,
	hooks *Hooks,
) (T, error) {
	result, err := fn(ctx)
	if err != nil {
		hooks.emitFallbackUsed(err)
		return fallbackVal, nil
	}

	return result, nil
}

// DoFallbackFunc executes fn. On error, calls fallbackFn with the error and
// returns its result.
//
//nolint:ireturn // generic type parameter T, not an interface
func DoFallbackFunc[T any](
	ctx context.Context,
	fn func(context.Context) (T, error),
	fallbackFn func(error) (T, error),
	hooks *Hooks,
) (T, error) {
	result, err := fn(ctx)
	if err != nil {
		hooks.emitFallbackUsed(err)

		//nolint:wrapcheck // fallback function's error returned as-is
		return fallbackFn(
			err,
		)
	}

	return result, nil
}
