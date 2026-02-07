package r8e

import "sort"

// Priority constants define the execution order for resilience patterns.
// Lower priority = outermost middleware (executed first).
const (
	priorityFallback       = 0 // outermost — last resort
	priorityStaleCache     = 1 // serves cached value on failure
	priorityTimeout        = 2 // global timeout
	priorityCircuitBreaker = 3
	priorityRateLimiter    = 4
	priorityBulkhead       = 5
	priorityRetry          = 6
	priorityHedge          = 7 // innermost — closest to user function
)

// PatternEntry holds a middleware with its priority for auto-ordering.
type PatternEntry[T any] struct {
	Priority int
	Name     string // for debugging: "fallback", "stale_cache", "timeout", etc.
	MW       Middleware[T]
}

// SortPatterns sorts pattern entries by priority (lowest first = outermost).
// Stable sort to preserve order of patterns with same priority.
func SortPatterns[T any](entries []PatternEntry[T]) []Middleware[T] {
	if len(entries) == 0 {
		return nil
	}

	// Copy to avoid mutating the caller's slice.
	sorted := make([]PatternEntry[T], len(entries))
	copy(sorted, entries)

	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Priority < sorted[j].Priority
	})

	mws := make([]Middleware[T], len(sorted))
	for i, e := range sorted {
		mws[i] = e.MW
	}
	return mws
}
