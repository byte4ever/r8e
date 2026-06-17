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

		// Live gauges at snapshot time.
		BulkheadInUse int64 `json:"bulkhead_in_use"` // slots currently held
		BulkheadCap   int64 `json:"bulkhead_cap"`    // configured slot capacity

		Criticality Criticality `json:"criticality"`
		Healthy     bool        `json:"healthy"`
		Saturated   bool        `json:"saturated"` // rate limiter has no tokens
	}

	// policyMetrics holds the atomic counters backing [PolicyMetrics]. It is
	// wired in via instrumented [Hooks], so every emitted lifecycle event
	// increments its counter regardless of whether the caller set that hook.
	policyMetrics struct {
		retries          atomic.Int64
		timeouts         atomic.Int64
		circuitOpens     atomic.Int64
		circuitCloses    atomic.Int64
		circuitHalfOpens atomic.Int64
		rateLimited      atomic.Int64
		bulkheadRejected atomic.Int64
		hedgesTriggered  atomic.Int64
		hedgesWon        atomic.Int64
		fallbacksUsed    atomic.Int64
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
	}
}

// Metrics returns a snapshot of this policy's cumulative counters and current
// live state (circuit state, rate-limiter saturation, bulkhead occupancy).
func (p *Policy[T]) Metrics() PolicyMetrics {
	health := p.HealthStatus()

	metrics := PolicyMetrics{
		Name:             p.name,
		Retries:          p.metrics.retries.Load(),
		Timeouts:         p.metrics.timeouts.Load(),
		CircuitOpens:     p.metrics.circuitOpens.Load(),
		CircuitCloses:    p.metrics.circuitCloses.Load(),
		CircuitHalfOpens: p.metrics.circuitHalfOpens.Load(),
		RateLimited:      p.metrics.rateLimited.Load(),
		BulkheadRejected: p.metrics.bulkheadRejected.Load(),
		HedgesTriggered:  p.metrics.hedgesTriggered.Load(),
		HedgesWon:        p.metrics.hedgesWon.Load(),
		FallbacksUsed:    p.metrics.fallbacksUsed.Load(),
		Criticality:      health.Criticality,
		Healthy:          health.Healthy,
	}

	if p.circuitBreaker != nil {
		metrics.CircuitState = p.circuitBreaker.State()
	}

	if p.rateLimiter != nil {
		metrics.Saturated = p.rateLimiter.Saturated()
	}

	if p.bulkhead != nil {
		metrics.BulkheadInUse = p.bulkhead.InUse()
		metrics.BulkheadCap = p.bulkhead.Cap()
	}

	return metrics
}
