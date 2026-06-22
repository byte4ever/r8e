package r8e

import "sort"

// PatternEntry holds a middleware with its priority for auto-ordering.
//
// Priority only establishes RELATIVE order: [SortPatterns] sorts ascending, so a
// lower value runs further out. The absolute integers are an internal ordering
// convention (the unexported priority* constants below) and are NOT a stable API
// — they are renumbered whenever a pattern is inserted. Do not hard-code a
// literal Priority expecting it to keep a fixed position relative to the built-in
// patterns; their values can shift between releases.
type PatternEntry[T any] struct {
	MW       Middleware[T]
	Name     string
	Priority int
}

// Priority constants define the execution order for resilience patterns.
// Lower priority = outermost middleware (executed first). These values are an
// internal convention (see [PatternEntry]): only their relative order is
// meaningful, and they are renumbered when a pattern is inserted.
const (
	priorityFallback          = 0 // outermost — last resort
	priorityCache             = 1 // read-through hit short-circuits the whole chain
	priorityCoalesce          = 2 // collapse duplicate concurrent calls before any work
	priorityTimeout           = 3 // global timeout (hard cancel)
	priorityTimeBudget        = 4 // total time budget shared across retry + hedge
	priorityThrottle          = 5 // proportional load shed before the breaker trips
	priorityCircuitBreaker    = 6
	priorityRateLimiter       = 7
	priorityBulkhead          = 8
	priorityConcurrencyBudget = 9 // tracks in-flight executions for the retry/hedge concurrency budget
	priorityRetry             = 10
	priorityHedge             = 11 // closest to user function among the durable patterns
	priorityRecover           = 12 // inside hedge so each hedge goroutine also recovers panics
	priorityChaos             = 13 // innermost — simulated downstream every pattern wraps and reacts to
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
