package r8e

import "context"

// Do is a convenience function that wraps a single function call with
// resilience
// patterns without creating a named [Policy]. It creates an anonymous policy
// internally and calls [Policy.Do]. The policy is not registered with any
// [Registry].
//
//nolint:ireturn,revive // generic type parameter T; Do name matches Policy.Do
// convention.
func Do[T any](
	ctx context.Context,
	fn func(context.Context) (T, error),
	opts ...any,
) (T, error) {
	p := NewPolicy[T]("", opts...)

	//nolint:wrapcheck // thin convenience wrapper; preserving original error
	return p.Do(
		ctx,
		fn,
	)
}
