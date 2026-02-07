package r8e

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Policy[T] — the central integration type
// ---------------------------------------------------------------------------

// Policy composes multiple resilience patterns (timeout, retry, circuit
// breaker, rate limiter, bulkhead, hedge, stale cache, fallback) behind a
// single [Do] method. Use [NewPolicy] with functional options to configure it.
//
// Pattern: Functional Options — configures Policy[T] via composable option
// functions; generic options use any to work around Go's generic type
// constraint on function signatures.
type Policy[T any] struct {
	name  string
	hooks Hooks
	clock Clock
	chain Middleware[T]

	// References to stateful patterns (needed later for health reporting).
	entries []PatternEntry[T]
	cb      *CircuitBreaker
	rl      *RateLimiter
	bh      *Bulkhead
	sc      *StaleCache[T]

	// Hierarchical health dependencies.
	deps []HealthReporter

	// Registry this policy is registered with (nil if anonymous or opted out).
	registry *Registry
}

// Name returns the policy's name.
func (p *Policy[T]) Name() string { return p.name }

// Do executes fn through the composed middleware chain.
func (p *Policy[T]) Do(ctx context.Context, fn func(context.Context) (T, error)) (T, error) {
	wrapped := p.chain(fn)
	return wrapped(ctx)
}

// ---------------------------------------------------------------------------
// Non-generic option descriptors — stored as any, interpreted by NewPolicy[T]
// ---------------------------------------------------------------------------

// policyOptionFunc is a non-generic option that modifies policySetup.
type policyOptionFunc func(*policySetup)

// policySetup holds non-generic configuration collected during NewPolicy.
type policySetup struct {
	clock    Clock
	hooks    Hooks
	registry *Registry
}

// timeoutDesc holds deferred timeout configuration.
type timeoutDesc struct {
	d time.Duration
}

// retryDesc holds deferred retry configuration.
type retryDesc struct {
	maxAttempts int
	strategy    BackoffStrategy
	opts        []RetryOption
}

// circuitBreakerDesc holds deferred circuit breaker configuration.
type circuitBreakerDesc struct {
	opts []CircuitBreakerOption
}

// rateLimitDesc holds deferred rate limiter configuration.
type rateLimitDesc struct {
	rate float64
	opts []RateLimitOption
}

// bulkheadDesc holds deferred bulkhead configuration.
type bulkheadDesc struct {
	maxConcurrent int
}

// hedgeDesc holds deferred hedge configuration.
type hedgeDesc struct {
	delay time.Duration
}

// staleCacheDesc holds deferred stale cache configuration.
type staleCacheDesc struct {
	ttl time.Duration
}

// fallbackDesc holds a type-erased static fallback value.
type fallbackDesc struct {
	val any
}

// fallbackFuncDesc holds a type-erased fallback function.
type fallbackFuncDesc struct {
	fn any // func(error) (T, error) stored as any
}

// ---------------------------------------------------------------------------
// With* functions — all return any
// ---------------------------------------------------------------------------

// WithClock sets the clock used by all resilience patterns within this policy.
func WithClock(c Clock) any {
	return policyOptionFunc(func(s *policySetup) {
		s.clock = c
	})
}

// WithHooks sets the lifecycle hooks for all resilience patterns within this policy.
func WithHooks(h Hooks) any {
	return policyOptionFunc(func(s *policySetup) {
		s.hooks = h
	})
}

// WithRegistry sets an explicit registry for the policy to register with.
// If not provided, named policies auto-register with DefaultRegistry.
func WithRegistry(reg *Registry) any {
	return policyOptionFunc(func(s *policySetup) {
		s.registry = reg
	})
}

// WithTimeout adds a timeout that cancels slow calls after d.
func WithTimeout(d time.Duration) any {
	return timeoutDesc{d: d}
}

// WithRetry adds retry logic with the given maximum attempts, backoff strategy,
// and optional retry configuration.
func WithRetry(maxAttempts int, strategy BackoffStrategy, opts ...RetryOption) any {
	return retryDesc{maxAttempts: maxAttempts, strategy: strategy, opts: opts}
}

// WithCircuitBreaker adds a circuit breaker that fast-fails when the downstream is unhealthy.
func WithCircuitBreaker(opts ...CircuitBreakerOption) any {
	return circuitBreakerDesc{opts: opts}
}

// WithRateLimit adds a token-bucket rate limiter that allows rate tokens per second.
func WithRateLimit(rate float64, opts ...RateLimitOption) any {
	return rateLimitDesc{rate: rate, opts: opts}
}

// WithBulkhead adds a concurrency limiter that rejects calls when all slots are in use.
func WithBulkhead(maxConcurrent int) any {
	return bulkheadDesc{maxConcurrent: maxConcurrent}
}

// WithHedge adds a hedged request that fires a second concurrent call after delay.
func WithHedge(delay time.Duration) any {
	return hedgeDesc{delay: delay}
}

// WithStaleCache adds a stale cache that serves the last-known-good value on failure.
func WithStaleCache(ttl time.Duration) any {
	return staleCacheDesc{ttl: ttl}
}

// WithFallback adds a static fallback value returned when the call fails.
// The value must match the Policy's type parameter T.
func WithFallback[T any](val T) any {
	return fallbackDesc{val: val}
}

// WithFallbackFunc adds a fallback function called with the error when the call fails.
// The function signature must be func(error) (T, error) matching the Policy's type parameter.
func WithFallbackFunc[T any](fn func(error) (T, error)) any {
	return fallbackFuncDesc{fn: fn}
}

// dependsOnDesc holds health reporters that this policy depends on.
type dependsOnDesc struct {
	reporters []HealthReporter
}

// DependsOn declares hierarchical health dependencies. If any dependency
// reports CriticalityCritical and is unhealthy, this policy's health
// status will be degraded.
func DependsOn(reporters ...HealthReporter) any {
	return dependsOnDesc{reporters: reporters}
}

// ---------------------------------------------------------------------------
// NewPolicy[T] — construct and wire up the policy
// ---------------------------------------------------------------------------

// NewPolicy creates a new [Policy] with the given name and options.
// Options are processed in two phases: first, non-generic options (clock, hooks)
// are collected; then, pattern descriptors build their middleware using the
// resolved clock and hooks. Patterns are auto-sorted by priority via
// [SortPatterns] before chaining.
func NewPolicy[T any](name string, opts ...any) *Policy[T] {
	var setup policySetup

	// Phase 1: Collect non-generic options to resolve clock and hooks first.
	for _, opt := range opts {
		if pof, ok := opt.(policyOptionFunc); ok {
			pof(&setup)
		}
	}

	// Default clock.
	if setup.clock == nil {
		setup.clock = RealClock{}
	}

	hooks := setup.hooks
	clock := setup.clock

	// Phase 2: Build middleware entries from pattern descriptors.
	var (
		entries []PatternEntry[T]
		cb      *CircuitBreaker
		rl      *RateLimiter
		bh      *Bulkhead
		sc      *StaleCache[T]
		deps    []HealthReporter
	)

	for _, opt := range opts {
		switch desc := opt.(type) {
		case policyOptionFunc:
			// Already processed in phase 1.

		case timeoutDesc:
			d := desc.d
			entries = append(entries, PatternEntry[T]{
				Priority: priorityTimeout,
				Name:     "timeout",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						return DoTimeout[T](ctx, d, next, &hooks)
					}
				},
			})

		case retryDesc:
			maxAttempts := desc.maxAttempts
			strategy := desc.strategy
			retryOpts := desc.opts
			entries = append(entries, PatternEntry[T]{
				Priority: priorityRetry,
				Name:     "retry",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						return DoRetry[T](ctx, maxAttempts, strategy, next, &hooks, clock, retryOpts...)
					}
				},
			})

		case circuitBreakerDesc:
			cb = NewCircuitBreaker(clock, &hooks, desc.opts...)
			cbRef := cb
			entries = append(entries, PatternEntry[T]{
				Priority: priorityCircuitBreaker,
				Name:     "circuit_breaker",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						if err := cbRef.Allow(); err != nil {
							var zero T
							return zero, err
						}
						val, err := next(ctx)
						if err != nil {
							cbRef.RecordFailure()
						} else {
							cbRef.RecordSuccess()
						}
						return val, err
					}
				},
			})

		case rateLimitDesc:
			rl = NewRateLimiter(desc.rate, clock, &hooks, desc.opts...)
			rlRef := rl
			entries = append(entries, PatternEntry[T]{
				Priority: priorityRateLimiter,
				Name:     "rate_limiter",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						if err := rlRef.Allow(ctx); err != nil {
							var zero T
							return zero, err
						}
						return next(ctx)
					}
				},
			})

		case bulkheadDesc:
			bh = NewBulkhead(desc.maxConcurrent, &hooks)
			bhRef := bh
			entries = append(entries, PatternEntry[T]{
				Priority: priorityBulkhead,
				Name:     "bulkhead",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						if err := bhRef.Acquire(); err != nil {
							var zero T
							return zero, err
						}
						defer bhRef.Release()
						return next(ctx)
					}
				},
			})

		case hedgeDesc:
			delay := desc.delay
			entries = append(entries, PatternEntry[T]{
				Priority: priorityHedge,
				Name:     "hedge",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						return DoHedge[T](ctx, delay, next, &hooks, clock)
					}
				},
			})

		case staleCacheDesc:
			sc = NewStaleCache[T](desc.ttl, clock, &hooks)
			scRef := sc
			entries = append(entries, PatternEntry[T]{
				Priority: priorityStaleCache,
				Name:     "stale_cache",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						return scRef.Do(ctx, next)
					}
				},
			})

		case fallbackDesc:
			val := desc.val.(T)
			entries = append(entries, PatternEntry[T]{
				Priority: priorityFallback,
				Name:     "fallback",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						return DoFallback[T](ctx, next, val, &hooks)
					}
				},
			})

		case fallbackFuncDesc:
			fn := desc.fn.(func(error) (T, error))
			entries = append(entries, PatternEntry[T]{
				Priority: priorityFallback,
				Name:     "fallback_func",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						return DoFallbackFunc[T](ctx, next, fn, &hooks)
					}
				},
			})

		case dependsOnDesc:
			deps = append(deps, desc.reporters...)
		}
	}

	// Sort by priority and chain.
	sorted := SortPatterns[T](entries)
	chain := Chain[T](sorted...)

	// Auto-register if policy has a name.
	var reg *Registry
	if name != "" {
		reg = setup.registry
		if reg == nil {
			reg = DefaultRegistry()
		}
	}

	p := &Policy[T]{
		name:     name,
		hooks:    hooks,
		clock:    clock,
		chain:    chain,
		entries:  entries,
		cb:       cb,
		rl:       rl,
		bh:       bh,
		sc:       sc,
		deps:     deps,
		registry: reg,
	}

	if reg != nil && name != "" {
		reg.Register(p)
	}

	return p
}
