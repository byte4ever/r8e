*[Read in English](README.md)*

# Exemple 12 — Hooks et observabilité

Illustre les 12 hooks de cycle de vie disponibles sur `Hooks`, en montrant
comment ils se déclenchent lors de l'exécution d'une politique.

## Ce que cet exemple illustre

Une structure `Hooks` complète est configurée avec tous les callbacks
disponibles, puis trois politiques exercent différents types de hooks :

### Hooks de retry

Une politique avec retry + fallback exécute une fonction qui échoue deux fois
avant de réussir. Le hook `OnRetry` se déclenche à chaque tentative de retry
avec le numéro de tentative et l'erreur qui l'a provoquée.

### Hooks de bulkhead

Une politique avec `WithBulkhead(1)` exécute un seul appel. Le hook
`OnBulkheadAcquired` se déclenche lorsque le slot est acquis, et
`OnBulkheadReleased` se déclenche lorsque le slot est libéré après
l'exécution.

### Hooks de fallback

Une politique avec retry + fallback exécute une fonction qui échoue
systématiquement. Une fois les retentatives épuisées, le hook
`OnFallbackUsed` se déclenche avec l'erreur finale avant que la valeur de
fallback ne soit renvoyée.

## Tous les hooks disponibles

| Hook | Quand il se déclenche |
|---|---|
| `OnRetry` | Avant chaque tentative de retry (avec le numéro de tentative et l'erreur) |
| `OnCircuitOpen` | Lorsque le circuit breaker passe à l'état ouvert |
| `OnCircuitClose` | Lorsque le circuit breaker revient à l'état fermé |
| `OnCircuitHalfOpen` | Lorsque le circuit breaker entre en état semi-ouvert (sonde) |
| `OnRateLimited` | Lorsqu'une requête est rejetée par le rate limiter |
| `OnBulkheadFull` | Lorsqu'une requête est rejetée car le bulkhead est à capacité maximale |
| `OnBulkheadAcquired` | Lorsqu'un slot du bulkhead est acquis |
| `OnBulkheadReleased` | Lorsqu'un slot du bulkhead est libéré |
| `OnTimeout` | Lorsqu'un appel dépasse son délai d'expiration |
| `OnHedgeTriggered` | Lorsque le délai de hedge s'écoule et qu'un second appel est lancé |
| `OnHedgeWon` | Lorsque l'appel de hedge se termine avant l'appel principal |
| `OnFallbackUsed` | Lorsque le fallback est invoqué (avec l'erreur déclencheuse) |

## Quand l'utiliser

- Alimenter les événements de retry et de circuit breaker dans les métriques
  (Prometheus, StatsD).
- Enregistrer les transitions d'état pour le débogage.
- Déclencher des alertes lors de l'ouverture du circuit breaker ou d'une
  utilisation répétée du fallback.

## Exécution

```bash
go run ./examples/12-hooks/
```
