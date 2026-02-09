*[Read in English](README.md)*

# Exemple 15 — Presets

Montre des ensembles d'options preconfigures pour les cas d'usage courants,
afin de ne pas avoir a configurer chaque pattern a partir de zero.

## Ce que cet exemple demontre

### StandardHTTPClient

`r8e.StandardHTTPClient()` retourne un slice d'options `[]any` avec :

- **Timeout :** 5 secondes
- **Retry :** 3 tentatives, backoff exponentiel de 100ms
- **Circuit breaker :** seuil de 5 echecs, recuperation en 30 secondes

Adapte aux clients HTTP a usage general pour lesquels des retries moderes et
un circuit breaker conservateur sont appropries.

### AggressiveHTTPClient

`r8e.AggressiveHTTPClient()` retourne des options avec :

- **Timeout :** 2 secondes
- **Retry :** 5 tentatives, backoff exponentiel de 50ms, delai maximum de
  5 secondes
- **Circuit breaker :** seuil de 3 echecs, recuperation en 15 secondes
- **Bulkhead :** 20 appels concurrents

Adapte aux services sensibles a la latence qui necessitent une detection plus
rapide des pannes, davantage de tentatives de retry et une protection de la
concurrence.

### Utilisation

Les presets retournent `[]any`, qui est decompresse dans `NewPolicy` avec
`...` :

```go
policy := r8e.NewPolicy[string]("my-api", r8e.StandardHTTPClient()...)
```

Vous pouvez ajouter des options supplementaires apres le preset pour
personnaliser davantage :

```go
opts := append(r8e.StandardHTTPClient(), r8e.WithFallback("default"))
policy := r8e.NewPolicy[string]("my-api", opts...)
```

## Concepts cles

| Concept | Detail |
|---|---|
| `StandardHTTPClient()` | Preset conservateur : timeout 5s, 3 retries, CB(5, 30s) |
| `AggressiveHTTPClient()` | Preset agressif : timeout 2s, 5 retries, CB(3, 15s), bulkhead(20) |
| Composable | Les presets retournent `[]any` — decompressee et extensible avec des options supplementaires |

## Execution

```bash
go run ./examples/15-presets/
```
