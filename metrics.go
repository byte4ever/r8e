package r8e

import "sync/atomic"

type (
	// PolicyMetrics is a point-in-time snapshot of a policy's runtime state and
	// cumulative counters. Obtain one via [Policy.Metrics] or, for every
	// registered policy at once, [Registry.Snapshot].
	PolicyMetrics struct {
		// Name is the policy name.
		Name string `json:"name"`
		// CircuitState is "closed", "open", or "half_open"; empty if the policy
		// has no circuit breaker.
		CircuitState string `json:"circuit_state"`

		// Cumulative counters since the policy was created.
		Retries          int64 `json:"retries"`
		Timeouts         int64 `json:"timeouts"`
		CircuitOpens     int64 `json:"circuit_opens"`
		CircuitCloses    int64 `json:"circuit_closes"`
		CircuitHalfOpens int64 `json:"circuit_half_opens"`
		RateLimited      int64 `json:"rate_limited"`
		BulkheadRejected int64 `json:"bulkhead_rejected"`
		HedgesTriggered  int64 `json:"hedges_triggered"`
		HedgesWon        int64 `json:"hedges_won"`
		FallbacksUsed    int64 `json:"fallbacks_used"`
		// RetryBudgetExceeded counts retries suppressed by the retry budget.
		RetryBudgetExceeded int64 `json:"retry_budget_exceeded"`
		// TimeBudgetExceeded counts retries stopped early by the time budget.
		TimeBudgetExceeded int64 `json:"time_budget_exceeded"`
		// CoalesceLeaders counts calls that ran a shared execution; together with
		// CoalesceFollowers it gives the deduplication ratio
		// followers/(leaders+followers).
		CoalesceLeaders int64 `json:"coalesce_leaders"`
		// CoalesceFollowers counts calls deduplicated into an in-flight execution
		// — the downstream calls coalescing saved.
		CoalesceFollowers int64 `json:"coalesce_followers"`
		// ConcurrencyRejected counts calls rejected by the adaptive concurrency
		// limiter because in-flight was at its current limit.
		ConcurrencyRejected int64 `json:"concurrency_rejected"`
		// Throttled counts calls shed locally by the adaptive throttler to
		// protect a struggling backend (see [WithAdaptiveThrottle]).
		Throttled int64 `json:"throttled"`
		// SlowCallRateExceeded counts circuit-breaker opens caused by the
		// slow-call rate reaching its threshold (see [SlowCallRate]), as opposed
		// to the consecutive-failure trip. It is a subset of CircuitOpens.
		SlowCallRateExceeded int64 `json:"slow_call_rate_exceeded"`
		// CacheHits counts calls served from the read-through cache without
		// executing the downstream work (fresh values and negative entries).
		CacheHits int64 `json:"cache_hits"`
		// CacheMisses counts calls for which the read-through cache had no fresh
		// value and the downstream work was executed; with CacheHits it gives the
		// hit ratio hits/(hits+misses).
		CacheMisses int64 `json:"cache_misses"`
		// CacheStores counts successful results written to the read-through cache.
		CacheStores int64 `json:"cache_stores"`
		// CacheStaleServed counts calls served a stale value after a downstream
		// failure (see [StaleIfError]) — a signal the downstream is failing while
		// the cache masks it.
		CacheStaleServed int64 `json:"cache_stale_served"`

		// Live gauges at snapshot time.
		BulkheadInUse int64 `json:"bulkhead_in_use"` // slots currently held
		BulkheadCap   int64 `json:"bulkhead_cap"`    // configured slot capacity
		// RetryBudgetTokens is the retry budget's current token level. It is 0
		// both for a policy with no retry budget and for one whose budget has
		// fully drained; read it together with whether the policy has a budget
		// (a non-zero capacity) to tell the two apart. When one RetryBudget is
		// shared across policies (WithSharedRetryBudget), every sharing policy
		// reports the same level under its own name — aggregate with max/avg,
		// not sum.
		RetryBudgetTokens float64 `json:"retry_budget_tokens"`
		// CoalesceInFlight is the number of distinct coalescing keys currently
		// executing; 0 when the policy has no coalescer.
		CoalesceInFlight int64 `json:"coalesce_in_flight"`
		// ConcurrencyLimit is the adaptive limiter's current concurrency limit;
		// 0 when the policy has no adaptive limiter.
		ConcurrencyLimit int64 `json:"concurrency_limit"`
		// ConcurrencyInFlight is the number of calls currently admitted by the
		// adaptive limiter; 0 when the policy has no adaptive limiter.
		ConcurrencyInFlight int64 `json:"concurrency_in_flight"`
		// ThrottleProbability is the adaptive throttler's current probability of
		// shedding a call, in [0, MaxRejectionRate]; 0 when the policy has no
		// throttler or it is forwarding all traffic.
		ThrottleProbability float64 `json:"throttle_probability"`
		// SlowCallRate is the current fraction of slow calls in the circuit
		// breaker's window, in [0, 1]; 0 when the policy has no breaker, slow-call
		// detection is off, or no calls have been observed (see [SlowCallRate]).
		SlowCallRate float64 `json:"slow_call_rate"`

		Criticality Criticality `json:"criticality"`
		Healthy     bool        `json:"healthy"`
		Saturated   bool        `json:"saturated"` // rate limiter has no tokens
	}

	// policyMetrics holds the atomic counters backing [PolicyMetrics]. It is
	// wired in via instrumented [Hooks], so every emitted lifecycle event
	// increments its counter regardless of whether the caller set that hook.
	policyMetrics struct {
		retries              atomic.Int64
		timeouts             atomic.Int64
		circuitOpens         atomic.Int64
		circuitCloses        atomic.Int64
		circuitHalfOpens     atomic.Int64
		rateLimited          atomic.Int64
		bulkheadRejected     atomic.Int64
		hedgesTriggered      atomic.Int64
		hedgesWon            atomic.Int64
		fallbacksUsed        atomic.Int64
		retryBudgetExceeded  atomic.Int64
		coalesceLeaders      atomic.Int64
		coalesceFollowers    atomic.Int64
		concurrencyRejected  atomic.Int64
		throttled            atomic.Int64
		slowCallRateExceeded atomic.Int64
		timeBudgetExceeded   atomic.Int64
		cacheHits            atomic.Int64
		cacheMisses          atomic.Int64
		cacheStores          atomic.Int64
		cacheStaleServed     atomic.Int64
	}

	// MetricsReporter is implemented by every [Policy]; [Registry.Snapshot]
	// uses it to collect metrics across policies with different type
	// parameters.
	MetricsReporter interface {
		// Name returns the policy's name.
		Name() string
		// Metrics returns a snapshot of the policy's counters and live state.
		Metrics() PolicyMetrics
	}
)

// instrument wraps the caller's hooks so each lifecycle event also increments
// the matching counter. The returned Hooks has non-nil fields for every
// counted event; uncounted events pass through unchanged.
//
// Pattern: Decorator — wraps the caller's Hooks with metric-counting behaviour
// while preserving the Hooks shape, so the wrapped value is substitutable for
// the original throughout NewPolicy.
func (m *policyMetrics) instrument(user *Hooks) Hooks {
	return Hooks{
		OnRetry: func(attempt int, err error) {
			m.retries.Add(1)

			if user.OnRetry != nil {
				user.OnRetry(attempt, err)
			}
		},
		OnCircuitOpen: func() {
			m.circuitOpens.Add(1)

			if user.OnCircuitOpen != nil {
				user.OnCircuitOpen()
			}
		},
		OnCircuitClose: func() {
			m.circuitCloses.Add(1)

			if user.OnCircuitClose != nil {
				user.OnCircuitClose()
			}
		},
		OnCircuitHalfOpen: func() {
			m.circuitHalfOpens.Add(1)

			if user.OnCircuitHalfOpen != nil {
				user.OnCircuitHalfOpen()
			}
		},
		OnRateLimited: func() {
			m.rateLimited.Add(1)

			if user.OnRateLimited != nil {
				user.OnRateLimited()
			}
		},
		OnBulkheadFull: func() {
			m.bulkheadRejected.Add(1)

			if user.OnBulkheadFull != nil {
				user.OnBulkheadFull()
			}
		},
		OnBulkheadAcquired: user.OnBulkheadAcquired,
		OnBulkheadReleased: user.OnBulkheadReleased,
		OnTimeout: func() {
			m.timeouts.Add(1)

			if user.OnTimeout != nil {
				user.OnTimeout()
			}
		},
		OnHedgeTriggered: func() {
			m.hedgesTriggered.Add(1)

			if user.OnHedgeTriggered != nil {
				user.OnHedgeTriggered()
			}
		},
		OnHedgeWon: func() {
			m.hedgesWon.Add(1)

			if user.OnHedgeWon != nil {
				user.OnHedgeWon()
			}
		},
		OnFallbackUsed: func(err error) {
			m.fallbacksUsed.Add(1)

			if user.OnFallbackUsed != nil {
				user.OnFallbackUsed(err)
			}
		},
		OnRetryBudgetExceeded: func() {
			m.retryBudgetExceeded.Add(1)

			if user.OnRetryBudgetExceeded != nil {
				user.OnRetryBudgetExceeded()
			}
		},
		OnCoalesceLeader: func() {
			m.coalesceLeaders.Add(1)

			if user.OnCoalesceLeader != nil {
				user.OnCoalesceLeader()
			}
		},
		OnCoalesceFollower: func() {
			m.coalesceFollowers.Add(1)

			if user.OnCoalesceFollower != nil {
				user.OnCoalesceFollower()
			}
		},
		OnConcurrencyRejected: func() {
			m.concurrencyRejected.Add(1)

			if user.OnConcurrencyRejected != nil {
				user.OnConcurrencyRejected()
			}
		},
		OnConcurrencyLimitChanged: user.OnConcurrencyLimitChanged,
		OnThrottled: func() {
			m.throttled.Add(1)

			if user.OnThrottled != nil {
				user.OnThrottled()
			}
		},
		OnSlowCallRateExceeded: func() {
			m.slowCallRateExceeded.Add(1)

			if user.OnSlowCallRateExceeded != nil {
				user.OnSlowCallRateExceeded()
			}
		},
		OnCacheHit: func() {
			m.cacheHits.Add(1)

			if user.OnCacheHit != nil {
				user.OnCacheHit()
			}
		},
		OnCacheMiss: func() {
			m.cacheMisses.Add(1)

			if user.OnCacheMiss != nil {
				user.OnCacheMiss()
			}
		},
		OnCacheStored: func() {
			m.cacheStores.Add(1)

			if user.OnCacheStored != nil {
				user.OnCacheStored()
			}
		},
		OnStaleServed: func() {
			m.cacheStaleServed.Add(1)

			if user.OnStaleServed != nil {
				user.OnStaleServed()
			}
		},
		OnTimeBudgetExceeded: func() {
			m.timeBudgetExceeded.Add(1)

			if user.OnTimeBudgetExceeded != nil {
				user.OnTimeBudgetExceeded()
			}
		},
	}
}

// Metrics returns a snapshot of this policy's cumulative counters and current
// live state (circuit state, rate-limiter saturation, bulkhead occupancy).
func (p *Policy[T]) Metrics() PolicyMetrics {
	health := p.HealthStatus()

	metrics := PolicyMetrics{
		Name:                 p.name,
		Retries:              p.metrics.retries.Load(),
		Timeouts:             p.metrics.timeouts.Load(),
		CircuitOpens:         p.metrics.circuitOpens.Load(),
		CircuitCloses:        p.metrics.circuitCloses.Load(),
		CircuitHalfOpens:     p.metrics.circuitHalfOpens.Load(),
		RateLimited:          p.metrics.rateLimited.Load(),
		BulkheadRejected:     p.metrics.bulkheadRejected.Load(),
		HedgesTriggered:      p.metrics.hedgesTriggered.Load(),
		HedgesWon:            p.metrics.hedgesWon.Load(),
		FallbacksUsed:        p.metrics.fallbacksUsed.Load(),
		RetryBudgetExceeded:  p.metrics.retryBudgetExceeded.Load(),
		CoalesceLeaders:      p.metrics.coalesceLeaders.Load(),
		CoalesceFollowers:    p.metrics.coalesceFollowers.Load(),
		ConcurrencyRejected:  p.metrics.concurrencyRejected.Load(),
		Throttled:            p.metrics.throttled.Load(),
		SlowCallRateExceeded: p.metrics.slowCallRateExceeded.Load(),
		TimeBudgetExceeded:   p.metrics.timeBudgetExceeded.Load(),
		CacheHits:            p.metrics.cacheHits.Load(),
		CacheMisses:          p.metrics.cacheMisses.Load(),
		CacheStores:          p.metrics.cacheStores.Load(),
		CacheStaleServed:     p.metrics.cacheStaleServed.Load(),
		Criticality:          health.Criticality,
		Healthy:              health.Healthy,
	}

	if p.circuitBreaker != nil {
		metrics.CircuitState = string(p.circuitBreaker.State())
		metrics.SlowCallRate = p.circuitBreaker.SlowCallFraction()
	}

	if p.rateLimiter != nil {
		metrics.Saturated = p.rateLimiter.Saturated()
	}

	if p.bulkhead != nil {
		metrics.BulkheadInUse = p.bulkhead.InUse()
		metrics.BulkheadCap = p.bulkhead.Cap()
	}

	if p.retryBudget != nil {
		metrics.RetryBudgetTokens = p.retryBudget.Tokens()
	}

	if p.coalescer != nil {
		metrics.CoalesceInFlight = int64(p.coalescer.InFlight())
	}

	if p.adaptive != nil {
		metrics.ConcurrencyLimit = int64(p.adaptive.Limit())
		metrics.ConcurrencyInFlight = int64(p.adaptive.InFlight())
	}

	if p.throttler != nil {
		metrics.ThrottleProbability = p.throttler.RejectionProbability()
	}

	return metrics
}
