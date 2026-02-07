package r8e

import "context"

// Do is a convenience function that wraps a single function call with resilience
// patterns without creating a named [Policy]. It creates an anonymous policy
// internally and calls [Policy.Do]. The policy is not registered with any
// [Registry].
func Do[T any](ctx context.Context, fn func(context.Context) (T, error), opts ...any) (T, error) {
	p := NewPolicy[T]("", opts...)
	return p.Do(ctx, fn)
}
