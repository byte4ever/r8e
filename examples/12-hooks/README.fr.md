*[Read in English](README.md)*

# Exemple 12 — Hooks et observabilite

Illustre les 12 hooks de cycle de vie disponibles sur `Hooks`, montrant
comment ils se declenchent lors de l'execution d'une politique.

## Ce que cet exemple illustre

Une structure `Hooks` complete est configuree avec tous les callbacks
disponibles, puis trois politiques exercent differents types de hooks :

### Hooks de retry

Une politique avec retry + fallback execute une fonction qui echoue deux fois
avant de reussir. Le hook `OnRetry` se declenche a chaque tentative de retry
avec le numero de tentative et l'erreur qui l'a provoquee.

### Hooks de bulkhead

Une politique avec `WithBulkhead(1)` execute un seul appel. Le hook
`OnBulkheadAcquired` se declenche lorsque le slot est acquis, et
`OnBulkheadReleased` se declenche lorsque le slot est libere apres
l'execution.

### Hooks de fallback

Une politique avec retry + fallback execute une fonction qui echoue
systematiquement. Une fois les retentatives epuisees, le hook
`OnFallbackUsed` se declenche avec l'erreur finale avant que la valeur de
fallback ne soit renvoyee.

## Tous les hooks disponibles

| Hook | Quand il se declenche |
|---|---|
| `OnRetry` | Avant chaque tentative de retry (avec le numero de tentative et l'erreur) |
| `OnCircuitOpen` | Lorsque le circuit breaker passe a l'etat ouvert |
| `OnCircuitClose` | Lorsque le circuit breaker revient a l'etat ferme |
| `OnCircuitHalfOpen` | Lorsque le circuit breaker entre en etat semi-ouvert (sonde) |
| `OnRateLimited` | Lorsqu'une requete est rejetee par le rate limiter |
| `OnBulkheadFull` | Lorsqu'une requete est rejetee car le bulkhead est a capacite maximale |
| `OnBulkheadAcquired` | Lorsqu'un slot du bulkhead est acquis |
| `OnBulkheadReleased` | Lorsqu'un slot du bulkhead est libere |
| `OnTimeout` | Lorsqu'un appel depasse son delai d'expiration |
| `OnHedgeTriggered` | Lorsque le delai de hedge s'ecoule et qu'un second appel est lance |
| `OnHedgeWon` | Lorsque l'appel de hedge se termine avant l'appel principal |
| `OnFallbackUsed` | Lorsque le fallback est invoque (avec l'erreur declencheuse) |

## Quand l'utiliser

- Alimenter les evenements de retry et de circuit breaker dans les metriques
  (Prometheus, StatsD).
- Enregistrer les transitions d'etat pour le debogage.
- Declencher des alertes lors de l'ouverture du circuit breaker ou d'une
  utilisation repetee du fallback.

## Execution

```bash
go run ./examples/12-hooks/
```
