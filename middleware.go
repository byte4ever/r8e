package r8e

import "context"

// Pattern: Decorator — each resilience pattern wraps the next, forming a
// composable chain where order determines execution semantics.

// Middleware wraps a function call with additional behavior.
// Each middleware receives the next function in the chain and returns a wrapped
// version.
type Middleware[T any] func(next func(context.Context) (T, error)) func(context.Context) (T, error)

// Chain composes multiple middlewares into a single middleware.
// Middlewares are applied in order: the first middleware is the outermost
// wrapper.
//
// Chain(a, b, c) produces a(b(c(next))) — a is outermost, c is innermost.
// Chain() with zero middlewares returns an identity middleware that passes
// through to next.
func Chain[T any](middlewares ...Middleware[T]) Middleware[T] {
	return func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
		for i := len(middlewares) - 1; i >= 0; i-- {
			next = middlewares[i](next)
		}

		return next
	}
}
