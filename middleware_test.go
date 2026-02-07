package r8e

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Single middleware wraps correctly
// ---------------------------------------------------------------------------

func TestChainSingleMiddlewareWrapsCorrectly(t *testing.T) {
	mw := Middleware[string](func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			result, err := next(ctx)
			return "wrapped(" + result + ")", err
		}
	})

	chained := Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		return "hello", nil
	})

	result, err := fn(context.Background())
	if err != nil {
		t.Fatalf("Chain() error = %v, want nil", err)
	}
	if result != "wrapped(hello)" {
		t.Fatalf("Chain() = %q, want %q", result, "wrapped(hello)")
	}
}

// ---------------------------------------------------------------------------
// Multiple middlewares execute in correct order
// ---------------------------------------------------------------------------

func TestChainMultipleMiddlewaresExecuteInCorrectOrder(t *testing.T) {
	var trace []string

	makeMW := func(name string) Middleware[string] {
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

	chained := Chain(mw1, mw2, mw3)
	fn := chained(func(_ context.Context) (string, error) {
		trace = append(trace, "handler")
		return "done", nil
	})

	result, err := fn(context.Background())
	if err != nil {
		t.Fatalf("Chain() error = %v, want nil", err)
	}
	if result != "done" {
		t.Fatalf("Chain() = %q, want %q", result, "done")
	}

	// Chain(mw1, mw2, mw3) produces mw1(mw2(mw3(next)))
	// So execution order is: mw1-before, mw2-before, mw3-before, handler, mw3-after, mw2-after, mw1-after
	want := []string{
		"mw1-before", "mw2-before", "mw3-before",
		"handler",
		"mw3-after", "mw2-after", "mw1-after",
	}

	if len(trace) != len(want) {
		t.Fatalf("trace length = %d, want %d; trace = %v", len(trace), len(want), trace)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("trace[%d] = %q, want %q; full trace = %v", i, trace[i], want[i], trace)
		}
	}
}

// ---------------------------------------------------------------------------
// Empty chain passes through to next
// ---------------------------------------------------------------------------

func TestChainEmptyPassesThrough(t *testing.T) {
	chained := Chain[string]()
	fn := chained(func(_ context.Context) (string, error) {
		return "passthrough", nil
	})

	result, err := fn(context.Background())
	if err != nil {
		t.Fatalf("Chain() error = %v, want nil", err)
	}
	if result != "passthrough" {
		t.Fatalf("Chain() = %q, want %q", result, "passthrough")
	}
}

// ---------------------------------------------------------------------------
// Chain preserves error propagation
// ---------------------------------------------------------------------------

func TestChainPreservesErrorPropagation(t *testing.T) {
	sentinel := errors.New("sentinel error")

	mw := Middleware[int](func(next func(context.Context) (int, error)) func(context.Context) (int, error) {
		return func(ctx context.Context) (int, error) {
			return next(ctx)
		}
	})

	chained := Chain(mw)
	fn := chained(func(_ context.Context) (int, error) {
		return 0, sentinel
	})

	_, err := fn(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Chain() error = %v, want %v", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// Chain preserves error from middleware itself
// ---------------------------------------------------------------------------

func TestChainPreservesMiddlewareError(t *testing.T) {
	mwErr := errors.New("middleware error")

	mw := Middleware[string](func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
		return func(_ context.Context) (string, error) {
			return "", mwErr
		}
	})

	chained := Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		return "should-not-reach", nil
	})

	_, err := fn(context.Background())
	if !errors.Is(err, mwErr) {
		t.Fatalf("Chain() error = %v, want %v", err, mwErr)
	}
}

// ---------------------------------------------------------------------------
// Chain preserves result values
// ---------------------------------------------------------------------------

func TestChainPreservesResultValues(t *testing.T) {
	mw := Middleware[int](func(next func(context.Context) (int, error)) func(context.Context) (int, error) {
		return func(ctx context.Context) (int, error) {
			result, err := next(ctx)
			return result * 2, err
		}
	})

	chained := Chain(mw)
	fn := chained(func(_ context.Context) (int, error) {
		return 21, nil
	})

	result, err := fn(context.Background())
	if err != nil {
		t.Fatalf("Chain() error = %v, want nil", err)
	}
	if result != 42 {
		t.Fatalf("Chain() = %d, want %d", result, 42)
	}
}

// ---------------------------------------------------------------------------
// Chain with multiple middlewares transforming results
// ---------------------------------------------------------------------------

func TestChainMultipleMiddlewaresTransformResult(t *testing.T) {
	prefix := func(p string) Middleware[string] {
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

	chained := Chain(prefix("a-"), prefix("b-"), prefix("c-"))
	fn := chained(func(_ context.Context) (string, error) {
		return "end", nil
	})

	// Chain(a-, b-, c-) produces a-(b-(c-(next)))
	// Innermost c- runs first on result: "c-end"
	// Then b-: "b-c-end"
	// Then a-: "a-b-c-end"
	result, err := fn(context.Background())
	if err != nil {
		t.Fatalf("Chain() error = %v, want nil", err)
	}
	if result != "a-b-c-end" {
		t.Fatalf("Chain() = %q, want %q", result, "a-b-c-end")
	}
}

// ---------------------------------------------------------------------------
// Empty chain identity middleware preserves errors
// ---------------------------------------------------------------------------

func TestChainEmptyPreservesError(t *testing.T) {
	sentinel := errors.New("pass-through error")
	chained := Chain[string]()
	fn := chained(func(_ context.Context) (string, error) {
		return "", sentinel
	})

	_, err := fn(context.Background())
	if !errors.Is(err, sentinel) {
		t.Fatalf("Chain() error = %v, want %v", err, sentinel)
	}
}

// ---------------------------------------------------------------------------
// Context propagates through chain
// ---------------------------------------------------------------------------

func TestChainContextPropagates(t *testing.T) {
	type ctxKey string
	key := ctxKey("test-key")

	mw := Middleware[string](func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			return next(context.WithValue(ctx, key, "injected"))
		}
	})

	chained := Chain(mw)
	fn := chained(func(ctx context.Context) (string, error) {
		v, _ := ctx.Value(key).(string)
		return v, nil
	})

	result, err := fn(context.Background())
	if err != nil {
		t.Fatalf("Chain() error = %v, want nil", err)
	}
	if result != "injected" {
		t.Fatalf("Chain() = %q, want %q", result, "injected")
	}
}

// ---------------------------------------------------------------------------
// Middleware type can short-circuit the chain
// ---------------------------------------------------------------------------

func TestChainMiddlewareCanShortCircuit(t *testing.T) {
	handlerCalled := false

	mw := Middleware[string](func(_ func(context.Context) (string, error)) func(context.Context) (string, error) {
		return func(_ context.Context) (string, error) {
			return "short-circuited", nil
		}
	})

	chained := Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		handlerCalled = true
		return "handler", nil
	})

	result, err := fn(context.Background())
	if err != nil {
		t.Fatalf("Chain() error = %v, want nil", err)
	}
	if result != "short-circuited" {
		t.Fatalf("Chain() = %q, want %q", result, "short-circuited")
	}
	if handlerCalled {
		t.Fatal("handler should not have been called when middleware short-circuits")
	}
}

// ---------------------------------------------------------------------------
// Chain is reusable with different functions
// ---------------------------------------------------------------------------

func TestChainReusableWithDifferentFunctions(t *testing.T) {
	var trace []string

	mw := Middleware[string](func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			trace = append(trace, "mw")
			return next(ctx)
		}
	})

	chained := Chain(mw)

	fn1 := chained(func(_ context.Context) (string, error) { return "fn1", nil })
	fn2 := chained(func(_ context.Context) (string, error) { return "fn2", nil })

	r1, _ := fn1(context.Background())
	r2, _ := fn2(context.Background())

	if r1 != "fn1" {
		t.Fatalf("fn1 result = %q, want %q", r1, "fn1")
	}
	if r2 != "fn2" {
		t.Fatalf("fn2 result = %q, want %q", r2, "fn2")
	}
	if len(trace) != 2 {
		t.Fatalf("trace length = %d, want 2; trace = %v", len(trace), trace)
	}
}

// ---------------------------------------------------------------------------
// Chain error propagation stops at intercepting middleware
// ---------------------------------------------------------------------------

func TestChainErrorInterceptedByMiddleware(t *testing.T) {
	mw := Middleware[string](func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			result, err := next(ctx)
			if err != nil {
				return "recovered", nil
			}
			return result, nil
		}
	})

	chained := Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		return "", errors.New("boom")
	})

	result, err := fn(context.Background())
	if err != nil {
		t.Fatalf("Chain() error = %v, want nil (error should be intercepted)", err)
	}
	if result != "recovered" {
		t.Fatalf("Chain() = %q, want %q", result, "recovered")
	}
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkChainSingle(b *testing.B) {
	mw := Middleware[string](func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			return next(ctx)
		}
	})
	chained := Chain(mw)
	fn := chained(func(_ context.Context) (string, error) {
		return "ok", nil
	})
	ctx := context.Background()

	for b.Loop() {
		_, _ = fn(ctx)
	}
}

func BenchmarkChainThree(b *testing.B) {
	makeMW := func() Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				return next(ctx)
			}
		}
	}

	chained := Chain(makeMW(), makeMW(), makeMW())
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
	logger := func(name string) Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				fmt.Println(name + " before")
				result, err := next(ctx)
				fmt.Println(name + " after")
				return result, err
			}
		}
	}

	chained := Chain(logger("outer"), logger("inner"))
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
	chained := Chain[string]()
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
	upper := Middleware[string](func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
		return func(ctx context.Context) (string, error) {
			result, err := next(ctx)
			if err != nil {
				return result, err
			}
			return strings.ToUpper(result), nil
		}
	})

	fn := upper(func(_ context.Context) (string, error) {
		return "hello world", nil
	})

	result, _ := fn(context.Background())
	fmt.Println(result)
	// Output:
	// HELLO WORLD
}
