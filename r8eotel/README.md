# r8e/r8eotel

[English] · [Français](README.fr.md)

OpenTelemetry bridge for [**r8e**](../README.md). It exposes r8e's per-policy
metrics as OTel instruments and wraps a policy with trace spans — kept in a
separate module so the **core r8e package stays dependency-free**.

## Install

```bash
go get github.com/byte4ever/r8e/r8eotel
```

## Metrics

`Register` creates observable instruments that report, per policy (labelled by a
`policy` attribute), the counters and live gauges from `r8e.Registry.Snapshot`.

```go
import (
    "go.opentelemetry.io/otel"
    "github.com/byte4ever/r8e"
    "github.com/byte4ever/r8e/r8eotel"
)

meter := otel.Meter("my-service")

// Every named policy auto-joins r8e.DefaultRegistry().
registration, err := r8eotel.Register(meter, r8e.DefaultRegistry())
if err != nil {
    return err
}
defer registration.Unregister()
```

All `r8e.policy.*` instruments are emitted: counters (`retries`, `circuit_opens`,
`timeouts`, `rate_limited`, `chaos_injected`, `cache_refreshes`, `circuit_ramps`,
…) and gauges (`circuit_state`, `latency_p50/p95/p99`, `adaptive_timeout`,
`adaptive_hedge_delay`, `ramp_recovery_fraction`, …). The instruments are
**observable** (pull-based), read on the OTel collection callback from the
registry snapshot — so there is no hot-path coupling to the SDK. See
[`examples/01-metrics`](examples/01-metrics).

```go
func Register(meter metric.Meter, reg MetricsSource) (metric.Registration, error)

type MetricsSource interface {
    Snapshot() []r8e.PolicyMetrics
}
```

`MetricsSource` is the consumer-side port: `*r8e.Registry` satisfies it, but so
can any custom reporter or test double.

## Tracing

`Trace` wraps an `r8e.Policy[T]` as a drop-in decorator that opens one **root
span** per `Do` (named after the policy) and one **child span** per fn invocation
— the initial attempt and each retry / hedge fork — so retry chains and hedge
races appear as individual, timed children in any OTel backend (Jaeger, Tempo, …).

```go
traced := r8eotel.Trace(policy, otel.GetTracerProvider())

result, err := traced.Do(ctx, fn) // same signature as policy.Do
```

The root span carries `r8e.policy`, `r8e.attempts`, and (on error)
`r8e.rejection_reason` — a short classification of the sentinel (`circuit_open`,
`retries_exhausted`, `timeout`, …). Each child carries `r8e.attempt.number`.
`TracedPolicy[T]` forwards every `Policy[T]` method (`Do`, `Name`, `Reconfigure`,
`Metrics`, `HealthStatus`). See [`examples/02-tracing`](examples/02-tracing).

```go
func Trace[T any](policy *r8e.Policy[T], tp trace.TracerProvider) *TracedPolicy[T]
```

## Notes

- Metrics gauges for client-side percentiles (`latency_p*`, `adaptive_*`,
  `ramp_recovery_fraction`) are deliberately observable gauges, consistent with
  the Micrometer precedent — they report a per-instance value and are not meant to
  aggregate across instances the way a histogram would.
- Both features are independent: use `Register` alone, `Trace` alone, or both.

See the [main r8e README](../README.md#observability) for the metrics and hooks
catalogue.
