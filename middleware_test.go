package r8e_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/byte4ever/r8e"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Single middleware wraps correctly
// ---------------------------------------------------------------------------

func TestChainSingleMiddlewareWrapsCorrectly(t *testing.T) {
	t.Parallel()

	mw := r8e.Middleware[string](
		func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				result, err := next(ctx)
				return "wrapped(" + result + ")", err
			}
		},
	)

	chained := r8e.Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		return "hello", nil
	})

	result, err := fn(context.Background())
	require.NoError(t, err)
	require.Equal(t, "wrapped(hello)", result)
}

// ---------------------------------------------------------------------------
// Multiple middlewares execute in correct order
// ---------------------------------------------------------------------------

func TestChainMultipleMiddlewaresExecuteInCorrectOrder(t *testing.T) {
	t.Parallel()

	var trace []string

	makeMW := func(name string) r8e.Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				trace = append(trace, name+"-before")
				result, err := next(ctx)
				trace = append(trace, name+"-after")
				return result, err
			}
		}
	}

	mw1 := makeMW("mw1")
	mw2 := makeMW("mw2")
	mw3 := makeMW("mw3")

	chained := r8e.Chain(mw1, mw2, mw3)
	fn := chained(func(_ context.Context) (string, error) {
		trace = append(trace, "handler")
		return "done", nil
	})

	result, err := fn(context.Background())
	require.NoError(t, err)
	require.Equal(t, "done", result)

	// Chain(mw1, mw2, mw3) produces mw1(mw2(mw3(next)))
	// So execution order is: mw1-before, mw2-before, mw3-before, handler,
	// mw3-after, mw2-after, mw1-after
	want := []string{
		"mw1-before", "mw2-before", "mw3-before",
		"handler",
		"mw3-after", "mw2-after", "mw1-after",
	}

	require.Equal(t, want, trace)
}

// ---------------------------------------------------------------------------
// Empty chain passes through to next
// ---------------------------------------------------------------------------

func TestChainEmptyPassesThrough(t *testing.T) {
	t.Parallel()

	chained := r8e.Chain[string]()
	fn := chained(func(_ context.Context) (string, error) {
		return "passthrough", nil
	})

	result, err := fn(context.Background())
	require.NoError(t, err)
	require.Equal(t, "passthrough", result)
}

// ---------------------------------------------------------------------------
// Chain preserves error propagation
// ---------------------------------------------------------------------------

func TestChainPreservesErrorPropagation(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("sentinel error")

	mw := r8e.Middleware[int](
		func(next func(context.Context) (int, error)) func(context.Context) (int, error) {
			return func(ctx context.Context) (int, error) {
				return next(ctx)
			}
		},
	)

	chained := r8e.Chain(mw)
	fn := chained(func(_ context.Context) (int, error) {
		return 0, sentinel
	})

	_, err := fn(context.Background())
	require.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// Chain preserves error from middleware itself
// ---------------------------------------------------------------------------

func TestChainPreservesMiddlewareError(t *testing.T) {
	t.Parallel()

	mwErr := errors.New("middleware error")

	mw := r8e.Middleware[string](
		func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(_ context.Context) (string, error) {
				return "", mwErr
			}
		},
	)

	chained := r8e.Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		return "should-not-reach", nil
	})

	_, err := fn(context.Background())
	require.ErrorIs(t, err, mwErr)
}

// ---------------------------------------------------------------------------
// Chain preserves result values
// ---------------------------------------------------------------------------

func TestChainPreservesResultValues(t *testing.T) {
	t.Parallel()

	mw := r8e.Middleware[int](
		func(next func(context.Context) (int, error)) func(context.Context) (int, error) {
			return func(ctx context.Context) (int, error) {
				result, err := next(ctx)
				return result * 2, err
			}
		},
	)

	chained := r8e.Chain(mw)
	fn := chained(func(_ context.Context) (int, error) {
		return 21, nil
	})

	result, err := fn(context.Background())
	require.NoError(t, err)
	require.Equal(t, 42, result)
}

// ---------------------------------------------------------------------------
// Chain with multiple middlewares transforming results
// ---------------------------------------------------------------------------

func TestChainMultipleMiddlewaresTransformResult(t *testing.T) {
	t.Parallel()

	prefix := func(p string) r8e.Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				result, err := next(ctx)
				if err != nil {
					return result, err
				}
				return p + result, nil
			}
		}
	}

	chained := r8e.Chain(prefix("a-"), prefix("b-"), prefix("c-"))
	fn := chained(func(_ context.Context) (string, error) {
		return "end", nil
	})

	// Chain(a-, b-, c-) produces a-(b-(c-(next)))
	// Innermost c- runs first on result: "c-end"
	// Then b-: "b-c-end"
	// Then a-: "a-b-c-end"
	result, err := fn(context.Background())
	require.NoError(t, err)
	require.Equal(t, "a-b-c-end", result)
}

// ---------------------------------------------------------------------------
// Empty chain identity middleware preserves errors
// ---------------------------------------------------------------------------

func TestChainEmptyPreservesError(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("pass-through error")
	chained := r8e.Chain[string]()
	fn := chained(func(_ context.Context) (string, error) {
		return "", sentinel
	})

	_, err := fn(context.Background())
	require.ErrorIs(t, err, sentinel)
}

// ---------------------------------------------------------------------------
// Context propagates through chain
// ---------------------------------------------------------------------------

func TestChainContextPropagates(t *testing.T) {
	t.Parallel()

	type ctxKey string
	key := ctxKey("test-key")

	mw := r8e.Middleware[string](
		func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				return next(context.WithValue(ctx, key, "injected"))
			}
		},
	)

	chained := r8e.Chain(mw)
	fn := chained(func(ctx context.Context) (string, error) {
		v, _ := ctx.Value(key).(string)
		return v, nil
	})

	result, err := fn(context.Background())
	require.NoError(t, err)
	require.Equal(t, "injected", result)
}

// ---------------------------------------------------------------------------
// Middleware type can short-circuit the chain
// ---------------------------------------------------------------------------

func TestChainMiddlewareCanShortCircuit(t *testing.T) {
	t.Parallel()

	handlerCalled := false

	mw := r8e.Middleware[string](
		func(_ func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(_ context.Context) (string, error) {
				return "short-circuited", nil
			}
		},
	)

	chained := r8e.Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		handlerCalled = true
		return "handler", nil
	})

	result, err := fn(context.Background())
	require.NoError(t, err)
	require.Equal(t, "short-circuited", result)
	require.False(t, handlerCalled,
		"handler should not have been called when middleware short-circuits")
}

// ---------------------------------------------------------------------------
// Chain is reusable with different functions
// ---------------------------------------------------------------------------

func TestChainReusableWithDifferentFunctions(t *testing.T) {
	t.Parallel()

	var trace []string

	mw := r8e.Middleware[string](
		func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				trace = append(trace, "mw")
				return next(ctx)
			}
		},
	)

	chained := r8e.Chain(mw)

	fn1 := chained(
		func(_ context.Context) (string, error) { return "fn1", nil },
	)
	fn2 := chained(
		func(_ context.Context) (string, error) { return "fn2", nil },
	)

	r1, _ := fn1(context.Background())
	r2, _ := fn2(context.Background())

	require.Equal(t, "fn1", r1)
	require.Equal(t, "fn2", r2)
	require.Len(t, trace, 2)
}

// ---------------------------------------------------------------------------
// Chain error propagation stops at intercepting middleware
// ---------------------------------------------------------------------------

func TestChainErrorInterceptedByMiddleware(t *testing.T) {
	t.Parallel()

	mw := r8e.Middleware[string](
		func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				result, err := next(ctx)
				if err != nil {
					return "recovered", nil
				}
				return result, nil
			}
		},
	)

	chained := r8e.Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		return "", errors.New("boom")
	})

	result, err := fn(context.Background())
	require.NoError(t, err)
	require.Equal(t, "recovered", result)
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkChainSingle(b *testing.B) {
	mw := r8e.Middleware[string](
		func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				return next(ctx)
			}
		},
	)
	chained := r8e.Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		return "ok", nil
	})
	ctx := context.Background()

	for b.Loop() {
		_, _ = fn(ctx)
	}
}

func BenchmarkChainThree(b *testing.B) {
	makeMW := func() r8e.Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				return next(ctx)
			}
		}
	}

	chained := r8e.Chain(makeMW(), makeMW(), makeMW())
	fn := chained(func(_ context.Context) (string, error) {
		return "ok", nil
	})
	ctx := context.Background()

	for b.Loop() {
		_, _ = fn(ctx)
	}
}

// ---------------------------------------------------------------------------
// Example
// ---------------------------------------------------------------------------

func ExampleChain() {
	// Create middlewares that log execution order.
	logger := func(name string) r8e.Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				fmt.Println(name + " before")
				result, err := next(ctx)
				fmt.Println(name + " after")
				return result, err
			}
		}
	}

	chained := r8e.Chain(logger("outer"), logger("inner"))
	fn := chained(func(_ context.Context) (string, error) {
		fmt.Println("handler")
		return "result", nil
	})

	result, _ := fn(context.Background())
	fmt.Println("got:", result)

	// Output:
	// outer before
	// inner before
	// handler
	// inner after
	// outer after
	// got: result
}

func ExampleChain_empty() {
	chained := r8e.Chain[string]()
	fn := chained(func(_ context.Context) (string, error) {
		return "passthrough", nil
	})
	result, _ := fn(context.Background())
	fmt.Println(result)
	// Output:
	// passthrough
}

func ExampleMiddleware() {
	// A middleware that uppercases the result.
	upper := r8e.Middleware[string](
		func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				result, err := next(ctx)
				if err != nil {
					return result, err
				}
				return strings.ToUpper(result), nil
			}
		},
	)

	fn := upper(func(_ context.Context) (string, error) {
		return "hello world", nil
	})

	result, _ := fn(context.Background())
	fmt.Println(result)
	// Output:
	// HELLO WORLD
}
