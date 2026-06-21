package r8e

import "sort"

// PatternEntry holds a middleware with its priority for auto-ordering.
type PatternEntry[T any] struct {
	MW       Middleware[T]
	Name     string
	Priority int
}

// Priority constants define the execution order for resilience patterns.
// Lower priority = outermost middleware (executed first).
const (
	priorityFallback       = 0 // outermost — last resort
	priorityCoalesce       = 1 // collapse duplicate concurrent calls before any work
	priorityTimeout        = 2 // global timeout (hard cancel)
	priorityTimeBudget     = 3 // total time budget shared across retry + hedge
	priorityCircuitBreaker = 4
	priorityRateLimiter    = 5
	priorityBulkhead       = 6
	priorityRetry          = 7
	priorityHedge          = 8 // innermost — closest to user function
)

// SortPatterns sorts pattern entries by priority (lowest first = outermost).
// Stable sort to preserve order of patterns with same priority.
func SortPatterns[T any](entries []PatternEntry[T]) []Middleware[T] {
	if len(entries) == 0 {
		return nil
	}

	// Copy to avoid mutating the caller's slice.
	sorted := make([]PatternEntry[T], 0, len(entries))
	sorted = append(sorted, entries...)

	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})

	mws := make([]Middleware[T], 0, len(sorted))
	for _, e := range sorted {
		mws = append(mws, e.MW)
	}

	return mws
}
