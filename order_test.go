package r8e

import (
	"context"
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// Patterns in random order get sorted correctly
// ---------------------------------------------------------------------------

func TestSortPatternsRandomOrderSortsCorrectly(t *testing.T) {
	var trace []string

	makeMW := func(name string) Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				trace = append(trace, name)
				return next(ctx)
			}
		}
	}

	// Deliberately out of order: retry, fallback, timeout, circuit_breaker
	entries := []PatternEntry[string]{
		{Priority: priorityRetry, Name: "retry", MW: makeMW("retry")},
		{Priority: priorityFallback, Name: "fallback", MW: makeMW("fallback")},
		{Priority: priorityTimeout, Name: "timeout", MW: makeMW("timeout")},
		{Priority: priorityCircuitBreaker, Name: "circuit_breaker", MW: makeMW("circuit_breaker")},
	}

	sorted := SortPatterns(entries)

	if len(sorted) != 4 {
		t.Fatalf("SortPatterns() returned %d middlewares, want 4", len(sorted))
	}

	// Execute the chain to verify ordering
	chained := Chain(sorted...)
	fn := chained(func(_ context.Context) (string, error) {
		trace = append(trace, "handler")
		return "ok", nil
	})

	_, err := fn(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Expect outermost (lowest priority) first
	want := []string{"fallback", "timeout", "circuit_breaker", "retry", "handler"}
	if len(trace) != len(want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("trace[%d] = %q, want %q; full trace = %v", i, trace[i], want[i], trace)
		}
	}
}

// ---------------------------------------------------------------------------
// Patterns already in correct order stay that way
// ---------------------------------------------------------------------------

func TestSortPatternsAlreadySortedStaysInOrder(t *testing.T) {
	var trace []string

	makeMW := func(name string) Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				trace = append(trace, name)
				return next(ctx)
			}
		}
	}

	entries := []PatternEntry[string]{
		{Priority: priorityFallback, Name: "fallback", MW: makeMW("fallback")},
		{Priority: priorityTimeout, Name: "timeout", MW: makeMW("timeout")},
		{Priority: priorityCircuitBreaker, Name: "circuit_breaker", MW: makeMW("circuit_breaker")},
		{Priority: priorityRetry, Name: "retry", MW: makeMW("retry")},
	}

	sorted := SortPatterns(entries)
	chained := Chain(sorted...)
	fn := chained(func(_ context.Context) (string, error) {
		trace = append(trace, "handler")
		return "ok", nil
	})

	_, err := fn(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{"fallback", "timeout", "circuit_breaker", "retry", "handler"}
	if len(trace) != len(want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("trace[%d] = %q, want %q; full trace = %v", i, trace[i], want[i], trace)
		}
	}
}

// ---------------------------------------------------------------------------
// Single pattern returns itself
// ---------------------------------------------------------------------------

func TestSortPatternsSinglePatternReturnsSelf(t *testing.T) {
	var called bool

	mw := Middleware[int](func(next func(context.Context) (int, error)) func(context.Context) (int, error) {
		return func(ctx context.Context) (int, error) {
			called = true
			return next(ctx)
		}
	})

	entries := []PatternEntry[int]{
		{Priority: priorityRetry, Name: "retry", MW: mw},
	}

	sorted := SortPatterns(entries)
	if len(sorted) != 1 {
		t.Fatalf("SortPatterns() returned %d middlewares, want 1", len(sorted))
	}

	fn := sorted[0](func(_ context.Context) (int, error) {
		return 42, nil
	})

	result, err := fn(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != 42 {
		t.Fatalf("result = %d, want 42", result)
	}
	if !called {
		t.Fatal("middleware was not called")
	}
}

// ---------------------------------------------------------------------------
// Empty slice returns empty
// ---------------------------------------------------------------------------

func TestSortPatternsEmptySliceReturnsEmpty(t *testing.T) {
	sorted := SortPatterns[string](nil)
	if len(sorted) != 0 {
		t.Fatalf("SortPatterns(nil) returned %d middlewares, want 0", len(sorted))
	}

	sorted = SortPatterns([]PatternEntry[string]{})
	if len(sorted) != 0 {
		t.Fatalf("SortPatterns([]) returned %d middlewares, want 0", len(sorted))
	}
}

// ---------------------------------------------------------------------------
// Same priority preserves insertion order (stable sort)
// ---------------------------------------------------------------------------

func TestSortPatternsStableSortPreservesInsertionOrder(t *testing.T) {
	var trace []string

	makeMW := func(name string) Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				trace = append(trace, name)
				return next(ctx)
			}
		}
	}

	// Two entries at the same priority — insertion order must be preserved
	entries := []PatternEntry[string]{
		{Priority: priorityRetry, Name: "retry-A", MW: makeMW("retry-A")},
		{Priority: priorityFallback, Name: "fallback", MW: makeMW("fallback")},
		{Priority: priorityRetry, Name: "retry-B", MW: makeMW("retry-B")},
	}

	sorted := SortPatterns(entries)
	chained := Chain(sorted...)
	fn := chained(func(_ context.Context) (string, error) {
		trace = append(trace, "handler")
		return "ok", nil
	})

	_, err := fn(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// fallback (0) first, then retry-A (6), then retry-B (6) — stable order preserved
	want := []string{"fallback", "retry-A", "retry-B", "handler"}
	if len(trace) != len(want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("trace[%d] = %q, want %q; full trace = %v", i, trace[i], want[i], trace)
		}
	}
}

// ---------------------------------------------------------------------------
// All priority constants have distinct values
// ---------------------------------------------------------------------------

func TestPriorityConstantsAreDistinct(t *testing.T) {
	priorities := map[string]int{
		"fallback":        priorityFallback,
		"stale_cache":     priorityStaleCache,
		"timeout":         priorityTimeout,
		"circuit_breaker": priorityCircuitBreaker,
		"rate_limiter":    priorityRateLimiter,
		"bulkhead":        priorityBulkhead,
		"retry":           priorityRetry,
		"hedge":           priorityHedge,
	}

	seen := make(map[int]string)
	for name, p := range priorities {
		if other, ok := seen[p]; ok {
			t.Fatalf("priority %d shared by %q and %q", p, other, name)
		}
		seen[p] = name
	}
}

// ---------------------------------------------------------------------------
// Priority constants have expected ordering
// ---------------------------------------------------------------------------

func TestPriorityConstantsOrdering(t *testing.T) {
	ordered := []struct {
		name     string
		priority int
	}{
		{"fallback", priorityFallback},
		{"stale_cache", priorityStaleCache},
		{"timeout", priorityTimeout},
		{"circuit_breaker", priorityCircuitBreaker},
		{"rate_limiter", priorityRateLimiter},
		{"bulkhead", priorityBulkhead},
		{"retry", priorityRetry},
		{"hedge", priorityHedge},
	}

	for i := 1; i < len(ordered); i++ {
		if ordered[i].priority <= ordered[i-1].priority {
			t.Fatalf("%s (priority %d) should be > %s (priority %d)",
				ordered[i].name, ordered[i].priority,
				ordered[i-1].name, ordered[i-1].priority)
		}
	}
}

// ---------------------------------------------------------------------------
// All eight patterns sorted from reverse order
// ---------------------------------------------------------------------------

func TestSortPatternsAllEightFromReverseOrder(t *testing.T) {
	var trace []string

	makeMW := func(name string) Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				trace = append(trace, name)
				return next(ctx)
			}
		}
	}

	// Reverse order (innermost to outermost)
	entries := []PatternEntry[string]{
		{Priority: priorityHedge, Name: "hedge", MW: makeMW("hedge")},
		{Priority: priorityRetry, Name: "retry", MW: makeMW("retry")},
		{Priority: priorityBulkhead, Name: "bulkhead", MW: makeMW("bulkhead")},
		{Priority: priorityRateLimiter, Name: "rate_limiter", MW: makeMW("rate_limiter")},
		{Priority: priorityCircuitBreaker, Name: "circuit_breaker", MW: makeMW("circuit_breaker")},
		{Priority: priorityTimeout, Name: "timeout", MW: makeMW("timeout")},
		{Priority: priorityStaleCache, Name: "stale_cache", MW: makeMW("stale_cache")},
		{Priority: priorityFallback, Name: "fallback", MW: makeMW("fallback")},
	}

	sorted := SortPatterns(entries)
	chained := Chain(sorted...)
	fn := chained(func(_ context.Context) (string, error) {
		trace = append(trace, "handler")
		return "ok", nil
	})

	_, err := fn(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{
		"fallback", "stale_cache", "timeout", "circuit_breaker",
		"rate_limiter", "bulkhead", "retry", "hedge", "handler",
	}
	if len(trace) != len(want) {
		t.Fatalf("trace = %v, want %v", trace, want)
	}
	for i := range want {
		if trace[i] != want[i] {
			t.Fatalf("trace[%d] = %q, want %q; full trace = %v", i, trace[i], want[i], trace)
		}
	}
}

// ---------------------------------------------------------------------------
// SortPatterns does not modify the original slice
// ---------------------------------------------------------------------------

func TestSortPatternsDoesNotModifyOriginal(t *testing.T) {
	makeMW := func(_ string) Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return next
		}
	}

	entries := []PatternEntry[string]{
		{Priority: priorityRetry, Name: "retry", MW: makeMW("retry")},
		{Priority: priorityFallback, Name: "fallback", MW: makeMW("fallback")},
	}

	// Remember original order
	origFirst := entries[0].Name
	origSecond := entries[1].Name

	_ = SortPatterns(entries)

	if entries[0].Name != origFirst || entries[1].Name != origSecond {
		t.Fatalf("SortPatterns modified original slice: got [%s, %s], want [%s, %s]",
			entries[0].Name, entries[1].Name, origFirst, origSecond)
	}
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkSortPatternsEight(b *testing.B) {
	makeMW := func() Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return next
		}
	}

	entries := []PatternEntry[string]{
		{Priority: priorityHedge, Name: "hedge", MW: makeMW()},
		{Priority: priorityRetry, Name: "retry", MW: makeMW()},
		{Priority: priorityBulkhead, Name: "bulkhead", MW: makeMW()},
		{Priority: priorityRateLimiter, Name: "rate_limiter", MW: makeMW()},
		{Priority: priorityCircuitBreaker, Name: "circuit_breaker", MW: makeMW()},
		{Priority: priorityTimeout, Name: "timeout", MW: makeMW()},
		{Priority: priorityStaleCache, Name: "stale_cache", MW: makeMW()},
		{Priority: priorityFallback, Name: "fallback", MW: makeMW()},
	}

	for b.Loop() {
		SortPatterns(entries)
	}
}

// ---------------------------------------------------------------------------
// Example
// ---------------------------------------------------------------------------

func ExampleSortPatterns() {
	makeMW := func(name string) Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				fmt.Println(name)
				return next(ctx)
			}
		}
	}

	// Patterns listed in wrong order by developer
	entries := []PatternEntry[string]{
		{Priority: priorityRetry, Name: "retry", MW: makeMW("retry")},
		{Priority: priorityFallback, Name: "fallback", MW: makeMW("fallback")},
		{Priority: priorityTimeout, Name: "timeout", MW: makeMW("timeout")},
	}

	sorted := SortPatterns(entries)
	chained := Chain(sorted...)
	fn := chained(func(_ context.Context) (string, error) {
		fmt.Println("handler")
		return "ok", nil
	})
	_, _ = fn(context.Background())

	// Output:
	// fallback
	// timeout
	// retry
	// handler
}
