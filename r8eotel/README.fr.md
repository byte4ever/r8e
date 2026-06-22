# r8e/r8eotel

[English](README.md) · [Français]

Pont OpenTelemetry pour [**r8e**](../README.fr.md). Il expose les métriques
par-policy de r8e comme instruments OTel et enveloppe une policy de spans de
trace — maintenu dans un module séparé afin que le **cœur de r8e reste sans
dépendance**.

## Installation

```bash
go get github.com/byte4ever/r8e/r8eotel
```

## Métriques

`Register` crée des instruments observables qui rapportent, par policy (étiquetés
par un attribut `policy`), les compteurs et gauges live de
`r8e.Registry.Snapshot`.

```go
import (
    "go.opentelemetry.io/otel"
    "github.com/byte4ever/r8e"
    "github.com/byte4ever/r8e/r8eotel"
)

meter := otel.Meter("my-service")

// Toute policy nommée rejoint automatiquement r8e.DefaultRegistry().
registration, err := r8eotel.Register(meter, r8e.DefaultRegistry())
if err != nil {
    return err
}
defer registration.Unregister()
```

Tous les instruments `r8e.policy.*` sont émis : compteurs (`retries`,
`circuit_opens`, `timeouts`, `rate_limited`, `chaos_injected`, `cache_refreshes`,
`circuit_ramps`, …) et gauges (`circuit_state`, `latency_p50/p95/p99`,
`adaptive_timeout`, `adaptive_hedge_delay`, `ramp_recovery_fraction`, …). Les
instruments sont **observables** (pull-based), lus dans le callback de collecte
OTel à partir du snapshot du registre — donc aucun couplage au SDK sur le chemin
critique. Voir [`examples/01-metrics`](examples/01-metrics).

```go
func Register(meter metric.Meter, reg MetricsSource) (metric.Registration, error)

type MetricsSource interface {
    Snapshot() []r8e.PolicyMetrics
}
```

`MetricsSource` est le port côté consommateur : `*r8e.Registry` le satisfait,
mais tout reporter personnalisé ou double de test le peut aussi.

## Tracing

`Trace` enveloppe une `r8e.Policy[T]` comme un décorateur transparent qui ouvre un
**span racine** par `Do` (nommé d'après la policy) et un **span enfant** par
invocation de fn — la tentative initiale et chaque retry / fork de hedge — afin
que les chaînes de retry et les courses de hedge apparaissent comme des enfants
individuels et chronométrés dans tout backend OTel (Jaeger, Tempo, …).

```go
traced := r8eotel.Trace(policy, otel.GetTracerProvider())

result, err := traced.Do(ctx, fn) // même signature que policy.Do
```

Le span racine porte `r8e.policy`, `r8e.attempts` et (en cas d'erreur)
`r8e.rejection_reason` — une classification courte de la sentinelle
(`circuit_open`, `retries_exhausted`, `timeout`, …). Chaque enfant porte
`r8e.attempt.number`. `TracedPolicy[T]` transmet toutes les méthodes de
`Policy[T]` (`Do`, `Name`, `Reconfigure`, `Metrics`, `HealthStatus`). Voir
[`examples/02-tracing`](examples/02-tracing).

```go
func Trace[T any](policy *r8e.Policy[T], tp trace.TracerProvider) *TracedPolicy[T]
```

## Notes

- Les gauges de percentiles côté client (`latency_p*`, `adaptive_*`,
  `ramp_recovery_fraction`) sont délibérément des gauges observables, conformément
  au précédent Micrometer — elles rapportent une valeur par instance et ne sont
  pas destinées à s'agréger entre instances comme le ferait un histogramme.
- Les deux fonctionnalités sont indépendantes : utilisez `Register` seul, `Trace`
  seul, ou les deux.

Voir le [README principal de r8e](../README.fr.md#observability) pour le
catalogue des métriques et des hooks.
