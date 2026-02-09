# r8e

**Arrêtez d'écrire des boucles de retry. Livrez des services résilients.**

r8e (_resilience_) vous offre timeout, retry, circuit breaker, rate limiter, bulkhead, requêtes spéculatives et fallback — le tout composable en une seule policy avec une ligne de code. Un cache périmé autonome avec des backends de cache interchangeables complète la chaîne. Zéro dépendance. Mécanismes internes lock-free. 100% de couverture de tests.

[![Go Reference](https://pkg.go.dev/badge/github.com/byte4ever/r8e.svg)](https://pkg.go.dev/github.com/byte4ever/r8e)
[![Go Report Card](https://goreportcard.com/badge/github.com/byte4ever/r8e)](https://goreportcard.com/report/github.com/byte4ever/r8e)
![Coverage](https://img.shields.io/badge/coverage-100%25-brightgreen)

```go
policy := r8e.NewPolicy[string]("payments",
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
    r8e.WithFallback("service unavailable"),
)
result, err := policy.Do(ctx, callPaymentGateway)
```

C'est tout. Les patterns sont automatiquement triés dans le bon ordre d'exécution. Le circuit breaker remonte l'état de santé vers votre endpoint Kubernetes `/readyz`. Les hooks alimentent votre pipeline de métriques. Et quand votre alerte sonne à 3h du matin, `r8e.ErrCircuitOpen` vous dit exactement ce qui s'est passé.

```bash
go get github.com/byte4ever/r8e
```

## Pourquoi r8e ?

- **Une policy, tous les patterns** — composez n'importe quelle combinaison ; r8e gère l'ordonnancement
- **Production-grade** — atomics lock-free, zéro allocation sur le chemin critique, 100% de couverture de tests
- **Kubernetes-native** — reporting de santé intégré avec dépendances hiérarchiques et handler `/readyz`
- **Observable** — 12 hooks de cycle de vie sur Policy, plus des hooks par StaleCache
- **Testable** — l'interface `Clock` permet de contrôler le temps dans les tests, fini les `time.Sleep` instables
- **Configurable** — définissez les policies en code, JSON, ou utilisez des presets prêts à l'emploi
- **Zéro dépendance** — uniquement la bibliothèque standard Go

## Fonctionnalités

| Pattern | Ce qu'il fait |
|---|---|
| **Timeout** | Annule les appels lents après un délai |
| **Retry** | Réessaie les erreurs transitoires avec backoff configurable (constant, exponentiel, linéaire, jitter) |
| **Circuit Breaker** | Échoue rapidement quand une dépendance est en panne, récupération automatique via sonde half-open |
| **Rate Limiter** | Contrôle de débit par token bucket (mode rejet ou blocage) |
| **Bulkhead** | Limitation de concurrence par sémaphore |
| **Requêtes spéculatives** | Lance un second appel après un délai pour réduire la latence de queue |
| **Stale Cache** | Sert la dernière valeur connue par clé en cas d'erreur (wrapper autonome avec backends de cache interchangeables) |
| **Fallback** | Valeur statique ou fonction de repli en dernier recours |

Plus : ordonnancement automatique des patterns, configuration JSON, presets, santé et readiness, hooks, `Clock` pour des tests déterministes.

## Démarrage rapide

```go
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/byte4ever/r8e"
)

func main() {
    policy := r8e.NewPolicy[string]("my-api",
        r8e.WithTimeout(2*time.Second),
        r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
        r8e.WithCircuitBreaker(),
    )

    result, err := policy.Do(context.Background(), func(ctx context.Context) (string, error) {
        return "bonjour, résilience !", nil
    })
    fmt.Println(result, err) // bonjour, résilience ! <nil>
}
```

## Patterns de résilience

### Timeout

Annule les appels lents après un délai. Si la fonction ne se termine pas à temps, `r8e.ErrTimeout` est retourné.

```go
policy := r8e.NewPolicy[string]("timeout-example",
    r8e.WithTimeout(2*time.Second),
)

result, err := policy.Do(ctx, func(ctx context.Context) (string, error) {
    // ctx sera annulé après 2s
    time.Sleep(5 * time.Second)
    return "trop lent", nil
})
// err == r8e.ErrTimeout
```

### Retry

Réessaie les erreurs transitoires avec des stratégies de backoff configurables. Les erreurs encapsulées avec `r8e.Permanent()` arrêtent immédiatement les retries.

**Stratégies de backoff :**

| Stratégie | Formule | Cas d'usage |
|---|---|---|
| `ConstantBackoff(d)` | `d` | Polling à intervalle fixe |
| `ExponentialBackoff(base)` | `base * 2^tentative` | Retry standard |
| `LinearBackoff(step)` | `step * (tentative+1)` | Montée progressive |
| `ExponentialJitterBackoff(base)` | `rand[0, base * 2^tentative]` | Prévenir l'effet de troupeau |

```go
policy := r8e.NewPolicy[string]("retry-example",
    r8e.WithRetry(4, r8e.ExponentialBackoff(200*time.Millisecond),
        r8e.MaxDelay(5*time.Second),
        r8e.PerAttemptTimeout(1*time.Second),
        r8e.RetryIf(func(err error) bool {
            return !errors.Is(err, errNotFound)
        }),
    ),
)
```

### Circuit Breaker

Échoue rapidement quand une dépendance est en mauvais état. Après `FailureThreshold` échecs consécutifs, le breaker s'ouvre. Après `RecoveryTimeout`, il passe en état half-open et autorise une sonde. `HalfOpenMaxAttempts` sondes réussies referment le breaker.

```go
policy := r8e.NewPolicy[string]("cb-example",
    r8e.WithCircuitBreaker(
        r8e.FailureThreshold(3),
        r8e.RecoveryTimeout(10*time.Second),
        r8e.HalfOpenMaxAttempts(2),
    ),
)

_, err := policy.Do(ctx, callDownstream)
if errors.Is(err, r8e.ErrCircuitOpen) {
    // la dépendance est en panne, échec rapide
}
```

### Rate Limiter

Limiteur de débit par token bucket. Le mode par défaut rejette avec `r8e.ErrRateLimited` ; le mode bloquant attend un jeton.

```go
// Mode rejet (par défaut) : 10 requêtes/seconde
policy := r8e.NewPolicy[string]("rl-reject",
    r8e.WithRateLimit(10),
)

// Mode bloquant : attend un jeton
policy = r8e.NewPolicy[string]("rl-blocking",
    r8e.WithRateLimit(10, r8e.RateLimitBlocking()),
)
```

### Bulkhead

Limite l'accès concurrent à une ressource. Retourne `r8e.ErrBulkheadFull` quand la capacité est atteinte.

```go
policy := r8e.NewPolicy[string]("bulkhead-example",
    r8e.WithBulkhead(5), // max 5 appels simultanés
)
```

### Requête spéculative

Lance un second appel concurrent après un délai. La première réponse gagne ; l'autre est annulée. Réduit la latence de queue.

```go
policy := r8e.NewPolicy[string]("hedge-example",
    r8e.WithHedge(100*time.Millisecond),
)
```

### Stale Cache

`StaleCache[K, V]` est un wrapper autonome de cache périmé par clé. En cas de succès, il stocke le résultat dans un backend `Cache[K, V]` interchangeable. En cas d'échec, il sert la dernière valeur connue pour cette clé (si elle est dans le TTL).

L'interface `Cache[K, V]` que les backends doivent implémenter :

```go
type Cache[K comparable, V any] interface {
    Get(key K) (V, bool)
    Set(key K, value V, ttl time.Duration)
    Delete(key K)
}
```

Utilisation avec l'adaptateur Otter :

```go
import (
    "github.com/byte4ever/r8e"
    otteradapter "github.com/byte4ever/r8e/otter"
)

// Créer le backend de cache
cache := otteradapter.New[string, string](r8e.CacheConfig{MaxSize: 10_000})

// Créer le stale cache avec hooks
sc := r8e.NewStaleCache(cache, 5*time.Minute,
    r8e.OnStaleServed[string, string](func(key string) {
        log.Printf("valeur périmée servie pour la clé %q", key)
    }),
    r8e.OnCacheRefreshed[string, string](func(key string) {
        log.Printf("cache rafraîchi pour la clé %q", key)
    }),
)

// Composer avec une Policy — appeler policy.Do dans staleCache.Do
policy := r8e.NewPolicy[string]("pricing-api",
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
)

result, err := sc.Do(ctx, "product-42", func(ctx context.Context, key string) (string, error) {
    return policy.Do(ctx, func(ctx context.Context) (string, error) {
        return fetchPrice(ctx, key)
    })
})
```

### Adaptateurs de cache

Les sous-packages adaptateurs implémentent `Cache[K, V]` pour les bibliothèques de cache populaires. Chacun est un module Go séparé pour que le package principal `r8e` reste sans dépendance.

| Adaptateur | Installation | Description |
|---|---|---|
| **Otter** | `go get github.com/byte4ever/r8e/otter` | Cache haute performance, sans contention, avec TTL par entrée |
| **Ristretto** | `go get github.com/byte4ever/r8e/ristretto` | Cache à admission de Dgraph avec éviction basée sur le coût |

Les deux adaptateurs acceptent un `r8e.CacheConfig` pour configurer la capacité :

```go
cfg := r8e.CacheConfig{MaxSize: 50_000}

otterCache := otteradapter.New[string, string](cfg)
risCache   := ristrettoadapter.New[string, string](cfg)
```

La configuration du cache peut aussi être chargée depuis un fichier JSON (voir [Configuration](#configuration)).

### Fallback

Dernière ligne de défense. Retourne une valeur statique ou appelle une fonction de repli quand tout le reste échoue.

```go
// Fallback statique
policy := r8e.NewPolicy[string]("static-fb",
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithFallback("valeur-par-defaut"),
)

// Fallback par fonction
policy = r8e.NewPolicy[string]("func-fb",
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithFallbackFunc(func(err error) (string, error) {
        return "calculé depuis : " + err.Error(), nil
    }),
)
```

## Composition de patterns

Combinez n'importe quels patterns dans une seule policy. `r8e` les trie automatiquement par priorité pour que l'ordre d'exécution soit toujours correct, quel que soit l'ordre de spécification des options.

```go
policy := r8e.NewPolicy[string]("composed",
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithTimeout(5*time.Second),
    r8e.WithCircuitBreaker(),
    r8e.WithBulkhead(10),
    r8e.WithRateLimit(100),
    r8e.WithFallback("fallback"),
)
```

### Ordre d'exécution

Les patterns sont triés automatiquement par priorité. Le middleware le plus externe s'exécute en premier :

```
Requête
  → Fallback          (le plus externe — attrape l'erreur finale)
    → Timeout         (deadline globale)
      → Circuit Breaker  (échec rapide si ouvert)
        → Rate Limiter   (contrôle du débit)
          → Bulkhead     (limite la concurrence)
            → Retry       (réessaie les erreurs transitoires)
              → Hedge     (le plus interne — lance des appels redondants)
                → fn()    (votre fonction)
```

StaleCache est autonome et enveloppe l'appel entier de la policy depuis l'extérieur (voir [Stale Cache](#stale-cache)).

## Classification des erreurs

Classifiez les erreurs pour contrôler le comportement de retry :

```go
// Les erreurs transitoires sont réessayées (c'est le défaut pour les erreurs non classifiées)
return r8e.Transient(fmt.Errorf("connexion réinitialisée"))

// Les erreurs permanentes arrêtent immédiatement les retries
return r8e.Permanent(fmt.Errorf("clé API invalide"))

// Vérifier la classification
r8e.IsTransient(err)  // true pour les erreurs non classifiées et explicitement transitoires
r8e.IsPermanent(err)  // true uniquement pour les erreurs explicitement permanentes
```

## Hooks et observabilité

Définissez des callbacks de cycle de vie pour intégrer vos systèmes de logging, métriques ou alertes :

```go
policy := r8e.NewPolicy[string]("observed",
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
    r8e.WithHooks(&r8e.Hooks{
        OnRetry:        func(attempt int, err error) { log.Printf("retry #%d: %v", attempt, err) },
        OnCircuitOpen:  func() { log.Println("circuit breaker ouvert") },
        OnCircuitClose: func() { log.Println("circuit breaker fermé") },
        OnTimeout:      func() { log.Println("requête expirée") },
        OnRateLimited:  func() { log.Println("débit limité") },
        OnFallbackUsed: func(err error) { log.Printf("fallback utilisé : %v", err) },
    }),
)
```

Hooks disponibles sur `Hooks` (12) : `OnRetry`, `OnCircuitOpen`, `OnCircuitClose`, `OnCircuitHalfOpen`, `OnRateLimited`, `OnBulkheadFull`, `OnBulkheadAcquired`, `OnBulkheadReleased`, `OnTimeout`, `OnHedgeTriggered`, `OnHedgeWon`, `OnFallbackUsed`.

StaleCache a ses propres hooks configurés via `StaleCacheOption` : `OnStaleServed[K,V]` et `OnCacheRefreshed[K,V]` (voir [Stale Cache](#stale-cache)).

## Santé et readiness

Les policies remontent automatiquement leur état de santé. Connectez un endpoint Kubernetes `/readyz` en quelques lignes :

```go
import "net/http"

// Les policies s'enregistrent automatiquement dans le registre par défaut
apiPolicy := r8e.NewPolicy[string]("api-gateway",
    r8e.WithCircuitBreaker(),
)
dbPolicy := r8e.NewPolicy[string]("database",
    r8e.WithCircuitBreaker(),
    r8e.DependsOn(apiPolicy), // dépendance hiérarchique
)

// Exposer l'endpoint de readiness
http.Handle("/readyz", r8e.ReadinessHandler(r8e.DefaultRegistry()))
```

Vérifier la santé par programmation :

```go
status := apiPolicy.HealthStatus()
fmt.Println(status.Healthy)     // true/false
fmt.Println(status.State)       // "healthy", "circuit_open", etc.
fmt.Println(status.Criticality) // CriticalityNone, CriticalityDegraded, CriticalityCritical
```

## Configuration

Chargez les policies depuis un fichier JSON :

```json
{
  "policies": {
    "payment-api": {
      "timeout": "2s",
      "circuit_breaker": {
        "failure_threshold": 5,
        "recovery_timeout": "30s"
      },
      "retry": {
        "max_attempts": 3,
        "backoff": "exponential",
        "base_delay": "100ms",
        "max_delay": "30s"
      },
      "rate_limit": 100,
      "bulkhead": 10
    }
  }
}
```

```go
reg, err := r8e.LoadConfig("config.json")
if err != nil {
    log.Fatal(err)
}

// Obtenir une policy typée — les options de config sont fusionnées avec les options en code
policy := r8e.GetPolicy[string](reg, "payment-api",
    r8e.WithFallback("service indisponible"),
)
```

Stratégies de backoff supportées en config : `"constant"`, `"exponential"`, `"linear"`, `"exponential_jitter"`.

Les backends de cache peuvent être configurés séparément via `LoadCacheConfig` :

```json
{
  "caches": {
    "pricing": {
      "ttl": "5m",
      "max_size": 10000
    }
  }
}
```

```go
cfg, err := r8e.LoadCacheConfig("caches.json", "pricing")
if err != nil {
    log.Fatal(err)
}
cache := otteradapter.New[string, string](cfg)
sc := r8e.NewStaleCache(cache, cfg.TTL)
```

## Configuration personnalisée

Les structs exportées `PolicyConfig`, `CircuitBreakerConfig` et `RetryConfig` portent des tags `json` et `yaml`, vous pouvez donc les embarquer dans votre propre config applicative et désérialiser depuis n'importe quel format. Appelez `BuildOptions` pour convertir une `PolicyConfig` en options fonctionnelles sans passer par `LoadConfig`.

```go
package main

import (
    "log"
    "os"

    "github.com/byte4ever/r8e"
    "gopkg.in/yaml.v3"
)

type AppConfig struct {
    Addr    string          `yaml:"addr"`
    Payment r8e.PolicyConfig `yaml:"payment"`
}

func main() {
    data, _ := os.ReadFile("app.yaml")

    var cfg AppConfig
    if err := yaml.Unmarshal(data, &cfg); err != nil {
        log.Fatal(err)
    }

    opts, err := r8e.BuildOptions(&cfg.Payment)
    if err != nil {
        log.Fatal(err)
    }

    policy := r8e.NewPolicy[string]("payment", opts...)
    _ = policy
}
```

## Presets

Ensembles d'options prêts à l'emploi pour les scénarios courants :

```go
// Standard : timeout 5s, 3 retries (backoff exp 100ms), CB (5 échecs, récupération 30s)
p := r8e.NewPolicy[string]("api", r8e.StandardHTTPClient()...)

// Agressif : timeout 2s, 5 retries (exp 50ms, cap 5s), CB (3 échecs, 15s), bulkhead(20)
p = r8e.NewPolicy[string]("fast-api", r8e.AggressiveHTTPClient()...)
```

## Fonction utilitaire

Pour des appels ponctuels sans créer une policy nommée :

```go
result, err := r8e.Do[string](ctx, myFunc,
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
)
```

## Tests

L'interface `Clock` permet des tests déterministes en substituant un faux temps :

```go
type Clock interface {
    Now() time.Time
    Since(t time.Time) time.Duration
    NewTimer(d time.Duration) Timer
}

// Utilisation dans les tests :
policy := r8e.NewPolicy[string]("test",
    r8e.WithClock(fakeClock),
    r8e.WithRetry(3, r8e.ExponentialBackoff(time.Second)),
)
```

## Exemples

Voir le répertoire [`examples/`](examples/) pour des exemples exécutables démontrant chaque fonctionnalité :

```bash
go run ./examples/01-quickstart/
go run ./examples/02-retry/
go run ./examples/03-circuit-breaker/
go run ./examples/04-timeout/
go run ./examples/05-rate-limiter/
go run ./examples/06-bulkhead/
go run ./examples/07-hedge/
go run ./examples/08-stale-cache/
go run ./examples/09-fallback/
go run ./examples/10-full-policy/
go run ./examples/11-error-classification/
go run ./examples/12-hooks/
go run ./examples/13-health-readiness/
go run ./examples/14-config/
go run ./examples/15-presets/
go run ./examples/16-convenience-do/
```

## Licence

MIT
