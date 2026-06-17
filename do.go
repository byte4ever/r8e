package r8e

import "context"

// Do is a convenience function that wraps a single function call with
// resilience
// patterns without creating a named [Policy]. It creates an anonymous policy
// internally and calls [Policy.Do]. The policy is not registered with any
// [Registry].
//
// confusing-naming rule flags Do against StaleCache.Do, but the shared name is
// the intended convention for the resilience entry point.
//
//nolint:ireturn,revive // ireturn: generic type parameter T; revive: the
func Do[T any](
	ctx context.Context,
	fn func(context.Context) (T, error),
	opts ...Option,
) (T, error) {
	p := NewPolicy[T]("", opts...)

	//nolint:wrapcheck // thin convenience wrapper; preserving original error
	return p.Do(
		ctx,
		fn,
	)
}
