// Package r8eotel bridges r8e policies to OpenTelemetry.
//
// Metrics: Register creates observable instruments that report, per policy
// (labelled by the "policy" attribute), the counters and live gauges from
// r8e.Registry.Snapshot.
//
// Tracing: Trace wraps an r8e.Policy[T] with trace spans — a root span per
// Do call (named after the policy) and a child span per fn invocation (initial
// attempt, each retry, each hedge fork), so retry chains and hedge races are
// visible in any OTel-compatible backend (Jaeger, Tempo, …).
//
// Keeping the OpenTelemetry dependency in this separate module lets the core
// r8e package stay dependency-free.
package r8eotel

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/byte4ever/r8e"
)

type (
	// MetricsSource is the consumer-side port r8eotel needs: a snapshot of
	// per-policy metrics. *r8e.Registry satisfies it, but so can any type that
	// can produce a snapshot (a test double or a custom reporter set), so the
	// bridge is not tied to the concrete registry.
	MetricsSource interface {
		// Snapshot returns the current per-policy metrics.
		Snapshot() []r8e.PolicyMetrics
	}

	observation struct {
		inst metric.Int64Observable
		get  func(*r8e.PolicyMetrics) int64
	}

	observationFloat64 struct {
		inst metric.Float64Observable
		get  func(*r8e.PolicyMetrics) float64
	}

	// instrumentBuilder accumulates instruments and the first error
	// encountered while creating them.
	instrumentBuilder struct {
		meter           metric.Meter
		err             error
		observations    []observation
		observationsF64 []observationFloat64
		instruments     []metric.Observable
	}
)

// Circuit-breaker state encoded as a gauge value.
const (
	circuitUnknownGauge  int64 = -1
	circuitClosedGauge   int64 = 0
	circuitHalfOpenGauge int64 = 1
	circuitOpenGauge     int64 = 2
)

// Register creates OpenTelemetry observable instruments on meter and a callback
// that reports metrics for every policy exposed by reg, labelled by the
// "policy" attribute. The returned [metric.Registration] can be used to stop
// reporting via its Unregister method.
//
//nolint:ireturn // returns the OpenTelemetry metric.Registration by design
func Register(meter metric.Meter, reg MetricsSource) (metric.Registration, error) {
	builder := &instrumentBuilder{meter: meter}

	builder.counter("r8e.policy.retries", "Total retry attempts",
		func(m *r8e.PolicyMetrics) int64 { return m.Retries })
	builder.counter("r8e.policy.timeouts", "Total timeouts",
		func(m *r8e.PolicyMetrics) int64 { return m.Timeouts })
	builder.counter("r8e.policy.circuit_opens", "Circuit-breaker open transitions",
		func(m *r8e.PolicyMetrics) int64 { return m.CircuitOpens })
	builder.counter("r8e.policy.circuit_closes", "Circuit-breaker close transitions",
		func(m *r8e.PolicyMetrics) int64 { return m.CircuitCloses })
	builder.counter("r8e.policy.circuit_half_opens", "Circuit-breaker half-open transitions",
		func(m *r8e.PolicyMetrics) int64 { return m.CircuitHalfOpens })
	builder.counter("r8e.policy.rate_limited", "Calls rejected by the rate limiter",
		func(m *r8e.PolicyMetrics) int64 { return m.RateLimited })
	builder.counter("r8e.policy.bulkhead_rejected", "Calls rejected by the bulkhead",
		func(m *r8e.PolicyMetrics) int64 { return m.BulkheadRejected })
	builder.counter("r8e.policy.bulkhead_timeouts", "Calls that timed out waiting for a bulkhead slot",
		func(m *r8e.PolicyMetrics) int64 { return m.BulkheadTimeouts })
	builder.counter("r8e.policy.hedges_triggered", "Hedged requests launched",
		func(m *r8e.PolicyMetrics) int64 { return m.HedgesTriggered })
	builder.counter("r8e.policy.hedges_won", "Hedged requests that won",
		func(m *r8e.PolicyMetrics) int64 { return m.HedgesWon })
	builder.counter("r8e.policy.fallbacks_used", "Fallbacks invoked",
		func(m *r8e.PolicyMetrics) int64 { return m.FallbacksUsed })
	builder.counter("r8e.policy.retry_budget_exceeded", "Retries suppressed by the retry budget",
		func(m *r8e.PolicyMetrics) int64 { return m.RetryBudgetExceeded })
	builder.counter("r8e.policy.time_budget_exceeded", "Retries stopped early by the time budget",
		func(m *r8e.PolicyMetrics) int64 { return m.TimeBudgetExceeded })
	builder.counter("r8e.policy.coalesce_leaders", "Calls that ran a shared coalesced execution",
		func(m *r8e.PolicyMetrics) int64 { return m.CoalesceLeaders })
	builder.counter("r8e.policy.coalesce_followers", "Calls deduplicated by request coalescing",
		func(m *r8e.PolicyMetrics) int64 { return m.CoalesceFollowers })
	builder.counter("r8e.policy.concurrency_rejected", "Calls rejected by the adaptive concurrency limiter",
		func(m *r8e.PolicyMetrics) int64 { return m.ConcurrencyRejected })
	builder.counter("r8e.policy.throttled", "Calls shed locally by the adaptive throttler",
		func(m *r8e.PolicyMetrics) int64 { return m.Throttled })
	builder.counter("r8e.policy.rate_adaptations", "AIMD adjustments to the rate limiter's refill rate",
		func(m *r8e.PolicyMetrics) int64 { return m.RateAdaptations })
	builder.counter("r8e.policy.slow_call_rate_exceeded", "Circuit-breaker opens triggered by the slow-call rate",
		func(m *r8e.PolicyMetrics) int64 { return m.SlowCallRateExceeded })
	builder.counter("r8e.policy.cache_hits", "Calls served from the read-through cache",
		func(m *r8e.PolicyMetrics) int64 { return m.CacheHits })
	builder.counter("r8e.policy.cache_misses", "Calls that missed the read-through cache and executed",
		func(m *r8e.PolicyMetrics) int64 { return m.CacheMisses })
	builder.counter("r8e.policy.cache_stores", "Successful results written to the read-through cache",
		func(m *r8e.PolicyMetrics) int64 { return m.CacheStores })
	builder.counter("r8e.policy.cache_stale_served", "Stale values served after a downstream failure",
		func(m *r8e.PolicyMetrics) int64 { return m.CacheStaleServed })
	builder.counter("r8e.policy.panics_recovered", "Panics caught by WithRecover and returned as errors",
		func(m *r8e.PolicyMetrics) int64 { return m.PanicsRecovered })
	builder.counter("r8e.policy.concurrency_budget_exceeded", "Retries/hedges shed by the concurrency budget",
		func(m *r8e.PolicyMetrics) int64 { return m.ConcurrencyBudgetExceeded })

	builder.gauge("r8e.policy.bulkhead_in_use", "Bulkhead slots currently held",
		func(m *r8e.PolicyMetrics) int64 { return m.BulkheadInUse })
	builder.gauge("r8e.policy.bulkhead_capacity", "Bulkhead slot capacity",
		func(m *r8e.PolicyMetrics) int64 { return m.BulkheadCap })
	builder.gauge("r8e.policy.bulkhead_queued", "Callers currently waiting for a bulkhead slot",
		func(m *r8e.PolicyMetrics) int64 { return m.BulkheadQueued })
	builder.gauge("r8e.policy.circuit_state", "Circuit state (-1=unknown, 0=closed, 1=half-open, 2=open)",
		circuitStateGauge)
	builder.gauge("r8e.policy.healthy", "1 if the policy is healthy, else 0",
		boolGauge(func(m *r8e.PolicyMetrics) bool { return m.Healthy }))
	builder.gauge("r8e.policy.saturated", "1 if the rate limiter has no tokens, else 0",
		boolGauge(func(m *r8e.PolicyMetrics) bool { return m.Saturated }))
	builder.gauge("r8e.policy.coalesce_in_flight", "Distinct coalescing keys currently executing",
		func(m *r8e.PolicyMetrics) int64 { return m.CoalesceInFlight })
	builder.gauge("r8e.policy.concurrency_limit", "Adaptive concurrency limiter's current limit",
		func(m *r8e.PolicyMetrics) int64 { return m.ConcurrencyLimit })
	builder.gauge("r8e.policy.concurrency_in_flight", "Calls currently admitted by the adaptive limiter",
		func(m *r8e.PolicyMetrics) int64 { return m.ConcurrencyInFlight })
	builder.gauge("r8e.policy.concurrency_budget_in_use", "Retries/hedges currently holding a concurrency-budget permit",
		func(m *r8e.PolicyMetrics) int64 { return m.ConcurrencyBudgetInUse })
	builder.gaugeFloat64("r8e.policy.retry_budget_tokens", "Retry budget tokens currently available",
		func(m *r8e.PolicyMetrics) float64 { return m.RetryBudgetTokens })
	builder.gaugeFloat64("r8e.policy.throttle_probability", "Adaptive throttler's current local-rejection probability",
		func(m *r8e.PolicyMetrics) float64 { return m.ThrottleProbability })
	builder.gaugeFloat64("r8e.policy.rate_limit", "Rate limiter's current refill rate in tokens per second",
		func(m *r8e.PolicyMetrics) float64 { return m.RateLimit })
	builder.gaugeFloat64("r8e.policy.slow_call_rate", "Current fraction of slow calls in the circuit-breaker window",
		func(m *r8e.PolicyMetrics) float64 { return m.SlowCallRate })
	builder.gaugeFloat64("r8e.policy.latency_p50", "Median Do() latency over the recent window, in seconds",
		func(m *r8e.PolicyMetrics) float64 { return m.LatencyP50.Seconds() })
	builder.gaugeFloat64("r8e.policy.latency_p95", "95th-percentile Do() latency over the recent window, in seconds",
		func(m *r8e.PolicyMetrics) float64 { return m.LatencyP95.Seconds() })
	builder.gaugeFloat64("r8e.policy.latency_p99", "99th-percentile Do() latency over the recent window, in seconds",
		func(m *r8e.PolicyMetrics) float64 { return m.LatencyP99.Seconds() })
	builder.gaugeFloat64("r8e.policy.adaptive_timeout", "Current adaptive timeout, in seconds",
		func(m *r8e.PolicyMetrics) float64 { return m.AdaptiveTimeout.Seconds() })
	builder.gaugeFloat64("r8e.policy.adaptive_hedge_delay", "Current adaptive hedge delay, in seconds",
		func(m *r8e.PolicyMetrics) float64 { return m.AdaptiveHedgeDelay.Seconds() })

	if builder.err != nil {
		return nil, builder.err
	}

	registration, err := meter.RegisterCallback(
		func(_ context.Context, observer metric.Observer) error {
			snapshot := reg.Snapshot()
			for i := range snapshot {
				attrs := metric.WithAttributes(
					attribute.String("policy", snapshot[i].Name),
				)
				for _, obs := range builder.observations {
					observer.ObserveInt64(obs.inst, obs.get(&snapshot[i]), attrs)
				}

				for _, obs := range builder.observationsF64 {
					observer.ObserveFloat64(obs.inst, obs.get(&snapshot[i]), attrs)
				}
			}

			return nil
		},
		builder.instruments...,
	)
	if err != nil {
		return nil, fmt.Errorf("r8e: register otel metrics callback: %w", err)
	}

	return registration, nil
}

func (b *instrumentBuilder) counter(
	name, desc string,
	get func(*r8e.PolicyMetrics) int64,
) {
	if b.err != nil {
		return
	}

	inst, err := b.meter.Int64ObservableCounter(name, metric.WithDescription(desc))
	if err != nil {
		b.err = err

		return
	}

	b.add(inst, get)
}

func (b *instrumentBuilder) gauge(
	name, desc string,
	get func(*r8e.PolicyMetrics) int64,
) {
	if b.err != nil {
		return
	}

	inst, err := b.meter.Int64ObservableGauge(name, metric.WithDescription(desc))
	if err != nil {
		b.err = err

		return
	}

	b.add(inst, get)
}

func (b *instrumentBuilder) gaugeFloat64(
	name, desc string,
	get func(*r8e.PolicyMetrics) float64,
) {
	if b.err != nil {
		return
	}

	inst, err := b.meter.Float64ObservableGauge(
		name,
		metric.WithDescription(desc),
	)
	if err != nil {
		b.err = err

		return
	}

	b.observationsF64 = append(
		b.observationsF64,
		observationFloat64{inst: inst, get: get},
	)
	b.instruments = append(b.instruments, inst)
}

func (b *instrumentBuilder) add(
	inst metric.Int64Observable,
	get func(*r8e.PolicyMetrics) int64,
) {
	b.observations = append(b.observations, observation{inst: inst, get: get})
	b.instruments = append(b.instruments, inst)
}

func circuitStateGauge(m *r8e.PolicyMetrics) int64 {
	switch m.CircuitState {
	case "", string(r8e.CircuitClosed):
		// Empty = the policy has no circuit breaker; report closed (no-trip).
		return circuitClosedGauge
	case string(r8e.CircuitHalfOpen):
		return circuitHalfOpenGauge
	case string(r8e.CircuitOpen):
		return circuitOpenGauge
	default:
		// An unrecognized state (e.g. a renamed constant) surfaces as -1 rather
		// than masquerading as the healthy "closed" value.
		return circuitUnknownGauge
	}
}

func boolGauge(pick func(*r8e.PolicyMetrics) bool) func(*r8e.PolicyMetrics) int64 {
	return func(m *r8e.PolicyMetrics) int64 {
		if pick(m) {
			return 1
		}

		return 0
	}
}
