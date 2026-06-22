*[Read in English](README.md)*

# Exemple 15 — Presets

Montre des ensembles d'options préconfigurés pour les cas d'usage courants,
afin de ne pas avoir à configurer chaque pattern à partir de zéro.

## Ce que cet exemple démontre

### StandardHTTPClient

`r8e.StandardHTTPClient()` retourne un slice d'options `[]any` avec :

- **Timeout :** 5 secondes
- **Retry :** 3 tentatives, backoff exponentiel de 100ms
- **Circuit breaker :** seuil de 5 échecs, récupération en 30 secondes

Adapté aux clients HTTP à usage général pour lesquels des retries modérés et
un circuit breaker conservateur sont appropriés.

### AggressiveHTTPClient

`r8e.AggressiveHTTPClient()` retourne des options avec :

- **Timeout :** 2 secondes
- **Retry :** 5 tentatives, backoff exponentiel de 50ms, délai maximum de
  5 secondes
- **Circuit breaker :** seuil de 3 échecs, récupération en 15 secondes
- **Bulkhead :** 20 appels concurrents

Adapté aux services sensibles à la latence qui nécessitent une détection plus
rapide des pannes, davantage de tentatives de retry et une protection de la
concurrence.

### Utilisation

Les presets retournent `[]any`, qui est décompressé dans `NewPolicy` avec
`...` :

```go
policy := r8e.NewPolicy[string]("my-api", r8e.StandardHTTPClient()...)
```

Vous pouvez ajouter des options supplémentaires après le preset pour
personnaliser davantage :

```go
opts := append(r8e.StandardHTTPClient(), r8e.WithFallback("default"))
policy := r8e.NewPolicy[string]("my-api", opts...)
```

## Concepts clés

| Concept | Détail |
|---|---|
| `StandardHTTPClient()` | Preset conservateur : timeout 5s, 3 retries, CB(5, 30s) |
| `AggressiveHTTPClient()` | Preset agressif : timeout 2s, 5 retries, CB(3, 15s), bulkhead(20) |
| Composable | Les presets retournent `[]any` — décompressé et extensible avec des options supplémentaires |

## Exécution

```bash
go run ./examples/15-presets/
```
