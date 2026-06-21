package r8e

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// SortPatterns produces the correct execution order
// ---------------------------------------------------------------------------

func TestSortPatternsExecutionOrder(t *testing.T) {
	t.Parallel()

	makeMW := func(trace *[]string, name string) Middleware[string] {
		return func(next func(context.Context) (string, error)) func(context.Context) (string, error) {
			return func(ctx context.Context) (string, error) {
				*trace = append(*trace, name)
				return next(ctx)
			}
		}
	}

	tests := []struct {
		name    string
		entries func(mk func(*[]string, string) Middleware[string], trace *[]string) []PatternEntry[string]
		want    []string
	}{
		{
			name: "random order sorts correctly",
			entries: func(mk func(*[]string, string) Middleware[string], trace *[]string) []PatternEntry[string] {
				return []PatternEntry[string]{
					{Priority: priorityRetry, Name: "retry", MW: mk(trace, "retry")},
					{Priority: priorityFallback, Name: "fallback", MW: mk(trace, "fallback")},
					{Priority: priorityTimeout, Name: "timeout", MW: mk(trace, "timeout")},
					{Priority: priorityCircuitBreaker, Name: "circuit_breaker", MW: mk(trace, "circuit_breaker")},
				}
			},
			want: []string{
				"fallback",
				"timeout",
				"circuit_breaker",
				"retry",
				"handler",
			},
		},
		{
			name: "already sorted stays in order",
			entries: func(mk func(*[]string, string) Middleware[string], trace *[]string) []PatternEntry[string] {
				return []PatternEntry[string]{
					{Priority: priorityFallback, Name: "fallback", MW: mk(trace, "fallback")},
					{Priority: priorityTimeout, Name: "timeout", MW: mk(trace, "timeout")},
					{Priority: priorityCircuitBreaker, Name: "circuit_breaker", MW: mk(trace, "circuit_breaker")},
					{Priority: priorityRetry, Name: "retry", MW: mk(trace, "retry")},
				}
			},
			want: []string{
				"fallback",
				"timeout",
				"circuit_breaker",
				"retry",
				"handler",
			},
		},
		{
			name: "stable sort preserves insertion order",
			entries: func(mk func(*[]string, string) Middleware[string], trace *[]string) []PatternEntry[string] {
				return []PatternEntry[string]{
					{Priority: priorityRetry, Name: "retry-A", MW: mk(trace, "retry-A")},
					{Priority: priorityFallback, Name: "fallback", MW: mk(trace, "fallback")},
					{Priority: priorityRetry, Name: "retry-B", MW: mk(trace, "retry-B")},
				}
			},
			want: []string{"fallback", "retry-A", "retry-B", "handler"},
		},
		{
			name: "all seven from reverse order",
			entries: func(mk func(*[]string, string) Middleware[string], trace *[]string) []PatternEntry[string] {
				return []PatternEntry[string]{
					{Priority: priorityHedge, Name: "hedge", MW: mk(trace, "hedge")},
					{Priority: priorityRetry, Name: "retry", MW: mk(trace, "retry")},
					{Priority: priorityBulkhead, Name: "bulkhead", MW: mk(trace, "bulkhead")},
					{Priority: priorityRateLimiter, Name: "rate_limiter", MW: mk(trace, "rate_limiter")},
					{Priority: priorityCircuitBreaker, Name: "circuit_breaker", MW: mk(trace, "circuit_breaker")},
					{Priority: priorityTimeout, Name: "timeout", MW: mk(trace, "timeout")},
					{Priority: priorityFallback, Name: "fallback", MW: mk(trace, "fallback")},
				}
			},
			want: []string{
				"fallback", "timeout", "circuit_breaker",
				"rate_limiter", "bulkhead", "retry", "hedge", "handler",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var trace []string

			entries := tt.entries(makeMW, &trace)

			sorted := SortPatterns(entries)
			require.Len(t, sorted, len(entries))

			chained := Chain(sorted...)
			fn := chained(func(_ context.Context) (string, error) {
				trace = append(trace, "handler")
				return "ok", nil
			})

			_, err := fn(context.Background())
			require.NoError(t, err)

			require.Equal(t, tt.want, trace)
		})
	}
}

// ---------------------------------------------------------------------------
// Single pattern returns itself
// ---------------------------------------------------------------------------

func TestSortPatternsSinglePatternReturnsSelf(t *testing.T) {
	t.Parallel()

	var called bool

	mw := Middleware[int](
		func(next func(context.Context) (int, error)) func(context.Context) (int, error) {
			return func(ctx context.Context) (int, error) {
				called = true
				return next(ctx)
			}
		},
	)

	entries := []PatternEntry[int]{
		{Priority: priorityRetry, Name: "retry", MW: mw},
	}

	sorted := SortPatterns(entries)
	require.Len(t, sorted, 1)

	fn := sorted[0](func(_ context.Context) (int, error) {
		return 42, nil
	})

	result, err := fn(context.Background())
	require.NoError(t, err)
	require.Equal(t, 42, result)
	require.True(t, called, "middleware was not called")
}

// ---------------------------------------------------------------------------
// Empty slice returns empty
// ---------------------------------------------------------------------------

func TestSortPatternsEmptySliceReturnsEmpty(t *testing.T) {
	t.Parallel()

	sorted := SortPatterns[string](nil)
	require.Empty(t, sorted)

	sorted = SortPatterns([]PatternEntry[string]{})
	require.Empty(t, sorted)
}

// ---------------------------------------------------------------------------
// All priority constants have distinct values
// ---------------------------------------------------------------------------

func TestPriorityConstantsAreDistinct(t *testing.T) {
	t.Parallel()

	priorities := map[string]int{
		"fallback":        priorityFallback,
		"cache":           priorityCache,
		"coalesce":        priorityCoalesce,
		"timeout":         priorityTimeout,
		"time_budget":     priorityTimeBudget,
		"circuit_breaker": priorityCircuitBreaker,
		"rate_limiter":    priorityRateLimiter,
		"bulkhead":        priorityBulkhead,
		"retry":           priorityRetry,
		"hedge":           priorityHedge,
	}

	seen := make(map[int]string)
	for name, p := range priorities {
		other, ok := seen[p]
		require.Falsef(t, ok, "priority %d shared by %q and %q", p, other, name)
		seen[p] = name
	}
}

// ---------------------------------------------------------------------------
// Priority constants have expected ordering
// ---------------------------------------------------------------------------

func TestPriorityConstantsOrdering(t *testing.T) {
	t.Parallel()

	ordered := []struct {
		name     string
		priority int
	}{
		{"fallback", priorityFallback},
		{"cache", priorityCache},
		{"coalesce", priorityCoalesce},
		{"timeout", priorityTimeout},
		{"time_budget", priorityTimeBudget},
		{"circuit_breaker", priorityCircuitBreaker},
		{"rate_limiter", priorityRateLimiter},
		{"bulkhead", priorityBulkhead},
		{"retry", priorityRetry},
		{"hedge", priorityHedge},
	}

	for i := 1; i < len(ordered); i++ {
		assert.Greaterf(t, ordered[i].priority, ordered[i-1].priority,
			"%s (priority %d) should be > %s (priority %d)",
			ordered[i].name, ordered[i].priority,
			ordered[i-1].name, ordered[i-1].priority)
	}
}

// ---------------------------------------------------------------------------
// SortPatterns does not modify the original slice
// ---------------------------------------------------------------------------

func TestSortPatternsDoesNotModifyOriginal(t *testing.T) {
	t.Parallel()

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

	require.Equal(t, origFirst, entries[0].Name)
	require.Equal(t, origSecond, entries[1].Name)
}

// ---------------------------------------------------------------------------
// Benchmark
// ---------------------------------------------------------------------------

func BenchmarkSortPatternsSeven(b *testing.B) {
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
		{
			Priority: priorityCircuitBreaker,
			Name:     "circuit_breaker",
			MW:       makeMW(),
		},
		{Priority: priorityTimeout, Name: "timeout", MW: makeMW()},
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
