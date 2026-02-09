package r8e

import (
	"context"
	"time"
)

// ---------------------------------------------------------------------------
// Policy[T] — the central integration type
// ---------------------------------------------------------------------------.

// Policy composes multiple resilience patterns (timeout, retry, circuit
// breaker, rate limiter, bulkhead, hedge, fallback) behind a
// single [Do] method. Use [NewPolicy] with functional options to configure it.
//
// Pattern: Functional Options — configures Policy[T] via composable option
// functions; generic options use any to work around Go's generic type
// constraint on function signatures.
type Policy[T any] struct {
	hooks          Hooks
	clock          Clock
	chain          Middleware[T]
	circuitBreaker *CircuitBreaker
	rateLimiter    *RateLimiter
	bulkhead       *Bulkhead
	registry       *Registry
	name           string
	entries        []PatternEntry[T]
	deps           []HealthReporter
}

// Name returns the policy's name.
func (p *Policy[T]) Name() string { return p.name }

// Do executes fn through the composed middleware chain.
//
//nolint:ireturn // generic type parameter T, not an interface
func (p *Policy[T]) Do(
	ctx context.Context,
	fn func(context.Context) (T, error),
) (T, error) {
	wrapped := p.chain(fn)

	//nolint:wrapcheck // middleware chain error returned as-is
	return wrapped(
		ctx,
	)
}

// ---------------------------------------------------------------------------
// Non-generic option descriptors — stored as any, interpreted by NewPolicy[T]
// ---------------------------------------------------------------------------.

//nolint:decorder // separate type block for internal descriptors; Policy[T]
// must remain distinct.
type (
	// policyOptionFunc is a non-generic option that modifies policySetup.
	policyOptionFunc func(*policySetup)

	// policySetup holds non-generic configuration collected during NewPolicy.
	policySetup struct {
		clock    Clock
		hooks    Hooks
		registry *Registry
	}

	// timeoutDesc holds deferred timeout configuration.
	timeoutDesc struct {
		d time.Duration
	}

	// retryDesc holds deferred retry configuration.
	retryDesc struct {
		strategy    BackoffStrategy
		opts        []RetryOption
		maxAttempts int
	}

	// circuitBreakerDesc holds deferred circuit breaker configuration.
	circuitBreakerDesc struct {
		opts []CircuitBreakerOption
	}

	// rateLimitDesc holds deferred rate limiter configuration.
	rateLimitDesc struct {
		opts []RateLimitOption
		rate float64
	}

	// bulkheadDesc holds deferred bulkhead configuration.
	bulkheadDesc struct {
		maxConcurrent int
	}

	// hedgeDesc holds deferred hedge configuration.
	hedgeDesc struct {
		delay time.Duration
	}

	// fallbackDesc holds a type-erased static fallback value.
	fallbackDesc struct {
		val any
	}

	// fallbackFuncDesc holds a type-erased fallback function.
	fallbackFuncDesc struct {
		fn any // func(error) (T, error) stored as any
	}
)

// ---------------------------------------------------------------------------
// With* functions — all return any
// ---------------------------------------------------------------------------.

// WithClock sets the clock used by all resilience patterns within this policy.
func WithClock(c Clock) any {
	return policyOptionFunc(func(s *policySetup) {
		s.clock = c
	})
}

// WithHooks sets the lifecycle hooks for all resilience patterns within this
// policy.
func WithHooks(h *Hooks) any {
	return policyOptionFunc(func(s *policySetup) {
		s.hooks = *h
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
func WithRetry(
	maxAttempts int,
	strategy BackoffStrategy,
	opts ...RetryOption,
) any {
	return retryDesc{maxAttempts: maxAttempts, strategy: strategy, opts: opts}
}

// WithCircuitBreaker adds a circuit breaker that fast-fails when the downstream
// is unhealthy.
func WithCircuitBreaker(opts ...CircuitBreakerOption) any {
	return circuitBreakerDesc{opts: opts}
}

// WithRateLimit adds a token-bucket rate limiter that allows rate tokens per
// second.
func WithRateLimit(rate float64, opts ...RateLimitOption) any {
	return rateLimitDesc{rate: rate, opts: opts}
}

// WithBulkhead adds a concurrency limiter that rejects calls when all slots are
// in use.
func WithBulkhead(maxConcurrent int) any {
	return bulkheadDesc{maxConcurrent: maxConcurrent}
}

// WithHedge adds a hedged request that fires a second concurrent call after
// delay.
func WithHedge(delay time.Duration) any {
	return hedgeDesc{delay: delay}
}

// WithFallback adds a static fallback value returned when the call fails.
// The value must match the Policy's type parameter T.
func WithFallback[T any](val T) any {
	return fallbackDesc{val: val}
}

// WithFallbackFunc adds a fallback function called with the error when the call
// fails. The function signature must be func(error) (T, error) matching the
// Policy's type parameter.
func WithFallbackFunc[T any](fn func(error) (T, error)) any {
	return fallbackFuncDesc{fn: fn}
}

// dependsOnDesc holds health reporters that this policy depends on.
//
//nolint:decorder // separated for readability near DependsOn function
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
// ---------------------------------------------------------------------------.

// NewPolicy creates a new [Policy] with the given name and options.
// Options are processed in two phases: first, non-generic options (clock,
// hooks)
// are collected; then, pattern descriptors build their middleware using the
// resolved clock and hooks. Patterns are auto-sorted by priority via
// [SortPatterns] before chaining.
//
//nolint:maintidx // large switch is inherent to the option-descriptor pattern
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
		entries        []PatternEntry[T]
		circuitBreaker *CircuitBreaker
		rateLimiter    *RateLimiter
		bulkhead       *Bulkhead
		deps           []HealthReporter
	)

	for _, opt := range opts {
		switch desc := opt.(type) {
		case policyOptionFunc:
			// Already processed in phase 1.

		case timeoutDesc:
			duration := desc.d

			entries = append(entries, PatternEntry[T]{
				Priority: priorityTimeout,
				Name:     "timeout",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						return DoTimeout[T](ctx, duration, next, &hooks)
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
						return DoRetry[T](ctx, next, RetryParams{
							MaxAttempts: maxAttempts,
							Strategy:    strategy,
							Hooks:       &hooks,
							Clock:       clock,
							Opts:        retryOpts,
						})
					}
				},
			})

		case circuitBreakerDesc:
			circuitBreaker = NewCircuitBreaker(clock, &hooks, desc.opts...)
			cbRef := circuitBreaker

			entries = append(entries, PatternEntry[T]{
				Priority: priorityCircuitBreaker,
				Name:     "circuit_breaker",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						if err := cbRef.Allow(); err != nil {
							var zero T

							return zero, err //nolint:wrapcheck // circuit breaker error returned as-is
						}

						val, err := next(ctx)
						if err != nil {
							cbRef.RecordFailure()
						} else {
							cbRef.RecordSuccess()
						}

						return val, err //nolint:wrapcheck // caller's error returned as-is
					}
				},
			})

		case rateLimitDesc:
			rateLimiter = NewRateLimiter(desc.rate, clock, &hooks, desc.opts...)
			rlRef := rateLimiter

			entries = append(entries, PatternEntry[T]{
				Priority: priorityRateLimiter,
				Name:     "rate_limiter",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						if err := rlRef.Allow(ctx); err != nil {
							var zero T

							return zero, err //nolint:wrapcheck // rate limiter error returned as-is
						}

						return next(ctx)
					}
				},
			})

		case bulkheadDesc:
			bulkhead = NewBulkhead(desc.maxConcurrent, &hooks)
			bhRef := bulkhead

			entries = append(entries, PatternEntry[T]{
				Priority: priorityBulkhead,
				Name:     "bulkhead",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						if err := bhRef.Acquire(); err != nil {
							var zero T

							return zero, err //nolint:wrapcheck // bulkhead error returned as-is
						}

						defer bhRef.Release()

						return next(ctx)
					}
				},
			})

		case hedgeDesc:
			hedgeParams := HedgeParams{
				Delay: desc.delay,
				Hooks: &hooks,
				Clock: clock,
			}

			entries = append(entries, PatternEntry[T]{
				Priority: priorityHedge,
				Name:     "hedge",
				MW: func(next func(context.Context) (T, error)) func(context.Context) (T, error) {
					return func(ctx context.Context) (T, error) {
						return DoHedge[T](ctx, next, hedgeParams)
					}
				},
			})

		case fallbackDesc:
			val, ok := desc.val.(T)
			if !ok {
				continue
			}

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
			fn, ok := desc.fn.(func(error) (T, error))
			if !ok {
				continue
			}

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

		default:
			// Unknown option type — silently ignored.
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

	policy := &Policy[T]{
		name:           name,
		hooks:          hooks,
		clock:          clock,
		chain:          chain,
		entries:        entries,
		circuitBreaker: circuitBreaker,
		rateLimiter:    rateLimiter,
		bulkhead:       bulkhead,
		deps:           deps,
		registry:       reg,
	}

	if reg != nil && name != "" {
		reg.Register(policy)
	}

	return policy
}
