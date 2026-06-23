# r8e

Une petite bibliothèque Go pour composer des patterns de résilience — timeout,
retry, circuit breaker, rate limiter, bulkhead, requêtes spéculatives et
fallback — en une seule policy. (Le nom abrège r(esilienc)e, dans l'esprit de
k8s.) Un cache périmé autonome avec des backends interchangeables complète la
chaîne. Le package principal n'a aucune dépendance externe.

[![Go Reference](https://pkg.go.dev/badge/github.com/byte4ever/r8e.svg)](https://pkg.go.dev/github.com/byte4ever/r8e)
[![Go Report Card](https://goreportcard.com/badge/github.com/byte4ever/r8e)](https://goreportcard.com/report/github.com/byte4ever/r8e)

```go
policy := r8e.NewPolicy[string]("payments",
    r8e.WithTimeout(2*time.Second),
    r8e.WithRetry(3, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithCircuitBreaker(),
    r8e.WithFallback("service unavailable"),
)
result, err := policy.Do(ctx, callPaymentGateway)
```

Les patterns sont automatiquement triés dans un ordre d'exécution raisonnable.
Un circuit breaker peut remonter l'état de santé vers un endpoint Kubernetes
`/readyz`, les hooks et les métriques alimentent un pipeline d'observabilité, et
des erreurs sentinelles comme `r8e.ErrCircuitOpen` rendent le mode de défaillance
explicite.

```bash
go get github.com/byte4ever/r8e
```

## État du projet

r8e est jeune (pré-1.0) : l'API peut encore changer et l'exposition en
production est limitée. Si vous avez besoin d'une bibliothèque mature et
largement adoptée, regardez [failsafe-go](https://github.com/failsafe-go/failsafe-go).
L'angle de r8e est une approche intégrée et opinionée — des policies nommées avec
métriques intégrées, reporting de santé optionnel et hot-reload de configuration.

## Points clés

- **Une policy, tous les patterns** — composez n'importe quelle combinaison ; r8e les ordonne pour vous
- **Concurrence** — rate limiter et bulkhead lock-free ; un circuit breaker linéarisable gardé par mutex
- **Reporting de santé** — intégration Kubernetes `/readyz` optionnelle avec dépendances hiérarchiques (`r8ehttp`)
- **Observabilité** — 34 hooks de cycle de vie, métriques par policy (compteurs + gauges live), un endpoint JSON et un pont OpenTelemetry (`r8eotel`)
- **Réglage à l'exécution** — hot-reload des paramètres des patterns (seuils de circuit breaker, limites de débit, timeouts…) sans redéploiement
- **Testable** — une interface `Clock` pour contrôler le temps dans les tests, sans `time.Sleep` instables
- **Configurable** — définissez les policies en code, JSON (`r8econf`), ou avec des presets
- **Cœur sans dépendance** — le package `r8e` n'utilise que la bibliothèque standard Go

## Fonctionnalités

| Pattern | Ce qu'il fait |
|---|---|
| **Timeout** | Annule les appels lents après un délai |
| **Budget de temps** | Un budget temps total sur toute la chaîne ; retry/hedge s'arrêtent avant de le dépasser |
| **Retry** | Réessaie les erreurs transitoires avec backoff configurable (constant, exponentiel, linéaire, jitter) |
| **Retry Budget** | Token bucket adaptatif qui throttle les retries quand les échecs dominent, évitant les retry storms |
| **Budget de concurrence** | Plafonne les retries/hedges concurrents comme fraction du trafic courant (avec un plancher), bornant le parallélisme des storms |
| **Circuit Breaker** | Échoue rapidement quand une dépendance est en panne, récupération automatique via sonde half-open |
| **Rate Limiter** | Contrôle de débit par token bucket (mode rejet ou blocage) |
| **Bulkhead** | Limitation de concurrence par sémaphore (limite fixe) |
| **Concurrence adaptative** | Limite de concurrence auto-ajustée depuis la latence observée (Gradient2 de Netflix) |
| **Throttle adaptatif** | Délestage probabiliste côté client selon le ratio accepts/requests observé (Google SRE), avant que le breaker ne déclenche |
| **Gouverneur de burn-rate SLO** | Délestage probabiliste piloté par la vitesse de consommation de l'error budget d'un SLO (burn rate multi-fenêtre) ; déleste d'abord le trafic sheddable pour préserver le budget du trafic critique |
| **Requêtes spéculatives** | Lance un second appel après un délai pour réduire la latence de queue |
| **Coalescing de requêtes** | Fusionne les appels identiques concurrents en une seule exécution partagée (singleflight), éliminant le cache stampede |
| **Cache read-through** | Mémoïse les résultats réussis par clé dans la chaîne ; les hits frais court-circuitent la chaîne, avec refresh-ahead, stale-if-error et negative caching |
| **Stale Cache** | Sert la dernière valeur connue par clé en cas d'erreur (wrapper autonome ; supplanté par le Cache read-through pour l'usage en chaîne) |
| **Fallback** | Valeur statique ou fonction de repli en dernier recours |
| **Recover** | Intercepte les panics de la fonction utilisateur et les retourne en tant que `*PanicError` ; retry, fallback ou circuit breaker peuvent alors les gérer au lieu de crasher |
| **Injection de chaos** | Injecte de façon probabiliste des fautes, de la latence, de faux résultats ou des comportements au cœur de la chaîne pour éprouver votre propre config de résilience (façon Polly v8 / Simmy), gating par appel pour un chaos canary sûr |

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

**Timeout adaptatif (piloté par les percentiles).** Par défaut le timeout est fixe. `AdaptiveTimeout(...)` dimensionne à la place chaque délai à partir d'une fenêtre glissante des latences **réussies** récentes — `clamp(percentile × multiplicateur, plancher, plafond)` — pour que le délai suive le temps de service réel du backend plutôt qu'une constante devinée. La durée passée à `WithTimeout` devient le **plafond** dur (l'adaptatif ne peut que resserrer en dessous, jamais le dépasser) et la valeur de repli utilisée tant que pas assez d'échantillons ne se sont accumulés, donc une policy froide ou à faible trafic utilise le timeout complet de l'opérateur.

```go
policy := r8e.NewPolicy[string]("adaptive-timeout",
    r8e.WithTimeout(time.Second,            // plafond dur + repli au démarrage
        r8e.AdaptiveTimeout(
            r8e.AdaptiveTimeoutPercentile(0.99), // défaut 0.99
            r8e.AdaptiveTimeoutMultiplier(2.0),  // défaut 2.0 (p99 × 2)
            r8e.AdaptiveTimeoutFloor(20*time.Millisecond), // défaut : aucun
            r8e.AdaptiveTimeoutMinSamples(20),   // défaut : démarrage à 20 échantillons
        ),
    ),
)
```

Seuls les appels réussis alimentent la fenêtre, donc un timeout ne gonfle jamais le percentile qui l'a fixé. C'est l'analogue latence→timeout du latence→limite de la [concurrence adaptative](#adaptive-concurrency). Observabilité : `Metrics().AdaptiveTimeout` (le timeout que la policy appliquerait actuellement) et la jauge OpenTelemetry `r8e.policy.adaptive_timeout` ; les déclenchements comptent toujours dans le compteur `Timeouts` et le hook `OnTimeout`. Voir [`examples/35-adaptive-timeout`](examples/35-adaptive-timeout).

**Délai de hedge adaptatif (piloté par les percentiles).** Par défaut le hedge se déclenche après un délai fixe. `AdaptiveHedge(...)` le déclenche à la place à un percentile en fenêtre glissante des latences **du primaire réussi** récentes — `clamp(percentile × multiplicateur, plancher, plafond)` — pour ne hedger que les vrais stragglers (par défaut les ~5 % les plus lents, la règle tail-at-scale de Google), gardant ainsi la charge redondante faible. La durée passée à `WithHedge` devient le **plafond** dur (l'adaptatif ne peut qu'avancer le hedge en dessous, jamais le retarder) et la valeur de repli au démarrage tant que pas assez d'échantillons ne se sont accumulés.

```go
policy := r8e.NewPolicy[string]("adaptive-hedge",
    r8e.WithHedge(500*time.Millisecond,        // plafond dur + repli au démarrage
        r8e.AdaptiveHedge(
            r8e.AdaptiveHedgePercentile(0.95), // défaut 0.95
            r8e.AdaptiveHedgeMultiplier(1.0),  // défaut 1.0 (déclenche au p95)
            r8e.AdaptiveHedgeFloor(5*time.Millisecond), // défaut : aucun
            r8e.AdaptiveHedgeMinSamples(20),   // défaut : démarrage à 20 échantillons
        ),
    ),
    r8e.WithConcurrencyBudget(r8e.MaxRatio(0.25), r8e.MinConcurrency(5)), // plafonne la charge ajoutée
)
```

Seule la complétion du **primaire** lui-même alimente la fenêtre — un hedge gagnant annule le primaire, dont la latence censurée est ignorée — donc un hedge ne peut jamais faire baisser le percentile qui a fixé son délai. C'est l'analogue latence→délai-de-hedge du latence→timeout du timeout adaptatif, et il se combine avec le [budget de concurrence](#budget-de-concurrence) pour borner la charge supplémentaire des hedges. Observabilité : `Metrics().AdaptiveHedgeDelay` (le délai que la policy appliquerait actuellement) et la jauge OpenTelemetry `r8e.policy.adaptive_hedge_delay` ; les déclenchements comptent toujours dans les compteurs `HedgesTriggered`/`HedgesWon` et les hooks `OnHedgeTriggered`/`OnHedgeWon`. Voir [`examples/36-adaptive-hedge`](examples/36-adaptive-hedge).

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

**Retry-After :** si l'erreur d'une tentative échouée implémente
`r8e.RetryAfterProvider` (`RetryAfter() (time.Duration, bool)`), le retry honore
ce délai (avec un jitter ±10%, plafonné par `MaxDelay`) à la place du backoff
calculé — l'attente exacte demandée par le serveur vaut mieux que toute
estimation. Attachez un indice fixe à n'importe quelle erreur avec
`r8e.RetryAfterError(err, d)`, ou implémentez l'interface vous-même ; l'adaptateur
[`httpx`](httpx) le fait automatiquement depuis un en-tête HTTP `429`/`503`
`Retry-After` (secondes ou HTTP-date). Voir [`examples/23-retry-after`](examples/23-retry-after).

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

**Taux d'appels lents (brownouts).** Au-delà des échecs consécutifs, le breaker peut s'ouvrir sur le taux d'appels *lents* — une dépendance qui répond, mais lentement. Activez-le avec `SlowCallRate(duration, rate)` : un appel dont la latence dépasse `duration` est « lent », et le breaker s'ouvre dès que cette fraction sur la fenêtre récente atteint `rate`. C'est indépendant et additif au trip sur échecs (le breaker s'ouvre sur le premier des deux qui se déclenche), avec une fenêtre count-based réglée via `SlowCallWindow` (défaut 100) et `SlowCallMinCalls` (défaut 10). Un appel réussi mais lent compte ; en half-open, une sonde lente rouvre comme une sonde échouée. Le hook dédié `OnSlowCallRateExceeded` et la gauge `SlowCallRate` exposent la cause. Voir [`examples/26-slow-call-breaker`](examples/26-slow-call-breaker).

```go
r8e.WithCircuitBreaker(
    r8e.FailureThreshold(5),                  // trébuche toujours sur les échecs
    r8e.SlowCallRate(2*time.Second, 0.5),     // …et sur >=50% d'appels lents
)
```

**Backoff adaptatif de récupération (opt-in).** Par défaut, le breaker sonde la dépendance à intervalle fixe (`RecoveryTimeout`). Avec `RecoveryBackoffMultiplier`, chaque sonde half-open échouée double (ou multiplie par le facteur configuré) l'attente avant la tentative suivante, réduisant la pression sur une dépendance en difficulté. `RecoveryMaxBackoff` plafonne la croissance. Le compteur se réinitialise à la valeur de base lorsque le breaker se referme avec succès. Voir [`examples/30-recovery-backoff`](examples/30-recovery-backoff).

```go
r8e.WithCircuitBreaker(
    r8e.RecoveryTimeout(5*time.Second),
    r8e.RecoveryBackoffMultiplier(2.0),   // 5s → 10s → 20s → …
    r8e.RecoveryMaxBackoff(60*time.Second),
)
```

**Récupération graduelle / slow-start (opt-in).** Par défaut, une sonde half-open réussie referme le breaker directement à 100 % du trafic. Avec `RampRecovery(window)`, le breaker passe plutôt dans l'état `CircuitRamping` et admet une fraction *croissante* du trafic sur `window` — ramenant en douceur une dépendance en convalescence vers la charge plutôt que de la noyer dès qu'elle paraît saine (slow-start de l'outlier-detection Envoy/Istio). La fraction admise suit `max(initial, timeFactor^(1/aggression))` où `timeFactor = elapsed/window` : `RampAggression` (défaut 1.0 = linéaire, > 1 = plus rapide au début) courbe la montée et `RampInitialFraction` (défaut 0.1) la plancher. Les appels rejetés pendant la montée renvoient `ErrCircuitRamping`, distinct de `ErrCircuitOpen` ; un appel échoué ou lent pendant la montée rouvre le breaker (et fait croître le backoff de récupération). Le hook `OnCircuitRamping` et la gauge `RampRecoveryFraction` exposent la montée. Voir [`examples/39-ramp-recovery`](examples/39-ramp-recovery).

```go
r8e.WithCircuitBreaker(
    r8e.RecoveryTimeout(200*time.Millisecond),
    r8e.RampRecovery(1*time.Second),   // montée 10 % → 100 % sur 1s après reprise
    r8e.RampInitialFraction(0.1),
)
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

**Débit adaptatif (AIMD).** Par défaut le débit de recharge est fixe. `AIMD(...)`
en fait une valeur de départ et un plafond ajustés par **additive-increase /
multiplicative-decrease** — la loi de contrôle de congestion derrière TCP. Après
chaque appel la policy réinjecte le résultat : un résultat signalant une surcharge
serveur multiplie le débit par `AIMDBackoff` (défaut `0.9`), tout autre résultat
ajoute `AIMDIncrease`, et le débit reste dans `[AIMDMinRate, AIMDMaxRate]`. Au
plus un ajustement est appliqué par `AIMDInterval` (défaut `1s`), donc une rafale
de rejets réduit le débit une seule fois au lieu de l'effondrer.

```go
policy := r8e.NewPolicy[Response]("api",
    r8e.WithRateLimit(100, // débit de départ et plafond
        r8e.AIMD(
            r8e.AIMDMinRate(10),                  // jamais sous 10/s (continuer à sonder)
            r8e.AIMDBackoff(0.5),                 // diviser le débit par deux sur surcharge
            r8e.AIMDIncrease(5),                  // rajouter 5/s par intervalle propre
            r8e.AIMDInterval(time.Second),        // au plus un mouvement par seconde
        ),
    ),
)
```

Par défaut un résultat est une surcharge seulement s'il est `ErrRateLimited` ou
porte un indice serveur `Retry-After` (un HTTP 429/503 exposé via le `StatusError`
de [`httpx`](httpx), ou tout `RetryAfterProvider`) ; une erreur métier laisse le
débit intact. Surchargez le signal avec `AIMDClassifier(func(error) bool)`. Les
paramètres numériques sont configurables en JSON (`AIMDConfig`, nécessite
`rate_limit`) et rechargeables à chaud ; le classifier est code-only.
Observabilité : le hook `OnRateAdapted` (déclenché avec le nouveau débit), le
compteur `RateAdaptations` et la jauge `RateLimit` (le débit live). Un
`RateLimiter` peut piloter l'AIMD en autonome via `NewRateLimiter` +
`RecordOutcome`. Voir [`examples/32-aimd-rate-limit`](examples/32-aimd-rate-limit).

### Bulkhead

Limite l'accès concurrent à une ressource. Retourne `r8e.ErrBulkheadFull` quand la capacité est atteinte.

```go
policy := r8e.NewPolicy[string]("bulkhead-example",
    r8e.WithBulkhead(5), // max 5 appels simultanés
)
```

**Attente FIFO bornée.** Par défaut un bulkhead plein rejette immédiatement. Avec `BulkheadMaxWait(d)`, un bulkhead plein met les appelants en file FIFO pendant au plus `d` (mesuré sur le `Clock` injecté), remettant chaque slot libéré à la tête de la file. La file est bornée par `BulkheadQueueDepth(n)` (défaut : la limite de concurrence) ; une fois pleine, les appelants sont rejetés immédiatement avec `ErrBulkheadFull`. Un appelant qui attend tout le max-wait abandonne avec `ErrBulkheadTimeout` (distinct du `ErrBulkheadFull` immédiat) ; un appelant dont le contexte est annulé en file retourne l'erreur du contexte. Observabilité : les hooks `OnBulkheadQueued` / `OnBulkheadTimeout`, le compteur `BulkheadTimeouts` et la gauge `BulkheadQueued`. Voir [`examples/27-bulkhead-wait`](examples/27-bulkhead-wait).

```go
r8e.WithBulkhead(10,
    r8e.BulkheadMaxWait(50*time.Millisecond), // met en file un bulkhead plein…
    r8e.BulkheadQueueDepth(20),               // …jusqu'à 20 en attente
)
```

> Le `Bulkhead.Acquire(ctx)` standalone prend un contexte (il peut bloquer sur l'attente bornée), s'alignant sur `RateLimiter.Allow(ctx)`.

**File à délai contrôlé (CoDel + LIFO adaptatif).** Au lieu de (ou en plus de) l'échéance fixe `BulkheadMaxWait`, `BulkheadCoDel(target, interval)` discipline la file d'attente selon le séjour *observé*, d'après la RFC 8289 et l'exécuteur folly de Facebook. Elle surveille le délai de file permanent (le séjour du plus ancien en attente) : tant qu'il reste inférieur ou égal à `target` la file est saine et sert en FIFO ; une fois resté au-dessus de `target` pendant tout un `interval` la file est **surchargée**, et dès lors les appelants ayant attendu au-delà du délai de largage (`2 × target`) sont largués avec `ErrCoDelShed` tandis que le slot libéré va au plus **récent** en attente (LIFO adaptatif) — gardant en mouvement le travail le plus frais et le plus susceptible d'être encore attendu, et abandonnant les rassis dont les clients ont probablement renoncé. Un seul échantillon revenu au niveau ou en dessous de `target` annule la surcharge et rétablit le FIFO. CoDel active l'attente à lui seul (un bulkhead avec seulement `BulkheadCoDel` met quand même en file) ; les défauts folly sont `target` 5 ms, `interval` 100 ms. Observabilité : le hook `OnCoDelShed`, le compteur `CoDelShed`, la gauge `CoDelLoad` ([0,1], délai permanent sur slough), le prédicat `Bulkhead.Overloaded()` et la condition de santé `bulkhead_overloaded` (dégradé). Voir [`examples/41-codel-queue`](examples/41-codel-queue).

```go
r8e.WithBulkhead(10,
    r8e.BulkheadCoDel(5*time.Millisecond, 100*time.Millisecond), // largue par séjour, sert en LIFO sous surcharge
    r8e.BulkheadQueueDepth(32),
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

> **Note :** pour un usage *à l'intérieur* d'une chaîne de policy, le [Cache read-through](#cache-read-through) (`WithCache`) le supplante désormais — il ajoute les hits read-through et le negative caching par-dessus le même comportement stale-on-error, en tant que pattern composable de premier ordre. `StaleCache` reste pour l'usage autonome, hors policy.

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
cache := otteradapter.MustNew[string, string](r8e.CacheConfig{MaxSize: 10_000})

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
| **[Otter](otter/README.fr.md)** | `go get github.com/byte4ever/r8e/otter` | Cache haute performance, sans contention, avec TTL par entrée |
| **[Ristretto](ristretto/README.fr.md)** | `go get github.com/byte4ever/r8e/ristretto` | Cache à admission de Dgraph avec éviction basée sur le coût |

Les deux adaptateurs acceptent un `r8e.CacheConfig` pour configurer la capacité :

```go
cfg := r8e.CacheConfig{MaxSize: 50_000}

otterCache := otteradapter.MustNew[string, string](cfg)
risCache   := ristrettoadapter.MustNew[string, string](cfg)
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
    → Cache           (read-through — un hit frais court-circuite la chaîne)
      → Coalesce      (fusionne les appels concurrents dupliqués)
        → Timeout         (deadline globale — annulation dure)
          → Budget temps   (budget total coopératif pour retry + hedge)
            → Gouverneur SLO (délestage pour préserver l'error budget du SLO)
              → Throttle adaptatif  (délestage proportionnel avant le déclenchement du breaker)
                → Circuit Breaker  (échec rapide si ouvert)
                  → Rate Limiter   (contrôle du débit)
                    → Bulkhead     (limite la concurrence — fixe, ou adaptative)
                      → Retry       (réessaie les erreurs transitoires, encadré par le retry budget)
                        → Hedge     (le plus interne — lance des appels redondants)
                          → fn()    (votre fonction)
```

Le retry budget n'est pas une étape séparée : il vit à l'intérieur de Retry et
throttle les tentatives de retry selon le ratio succès/échec courant (voir [Retry Budget](#retry-budget)).

Le cache se place juste à l'intérieur de Fallback et à l'extérieur de tout le
reste : un hit frais retourne sans exécuter coalesce, timeout ni aucune étape en
aval, et une valeur de fallback n'est jamais mise en cache (seul un vrai succès en
aval l'est). Le coalescing se place juste à l'intérieur du cache : une rafale de
miss sur une clé chaude partage un seul passage par timeout, circuit breaker, rate
limiter, bulkhead, retry et hedge — tandis que chaque appelant garde son propre
fallback (voir [Cache read-through](#cache-read-through) et [Coalescing de requêtes](#coalescing-de-requêtes)).

StaleCache est autonome et enveloppe l'appel entier de la policy depuis l'extérieur (voir [Stale Cache](#stale-cache)).

## Budget de temps

`WithTimeBudget` fixe un budget temps **total** pour tout l'appel, partagé entre
retry et hedge. Avant chaque retry, si le backoff seul dépasserait le budget
restant, le retry **s'arrête tôt** avec `ErrTimeBudgetExceeded` (enveloppant la
vraie erreur downstream) au lieu de dormir puis lancer une tentative qui ne peut
pas finir à temps ; un hedge n'est pas lancé une fois le budget épuisé.

```go
policy := r8e.NewPolicy[Response]("svc",
    r8e.WithRetry(5, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithTimeBudget(350*time.Millisecond), // plafonne le temps total
)
```

C'est **plus serré qu'un timeout par tentative** : `PerAttemptTimeout` borne
chaque tentative indépendamment (5 × 1s = jusqu'à 5s), tandis que le budget
plafonne la *somme*. Le budget est **coopératif** et mesuré contre le `Clock` de
la policy : il décide si plus de travail démarre mais n'annule pas une tentative
en cours — associez-le à `WithTimeout` (deadline dure) pour borner un appel
bloqué. Le budget ne contrôle que retry et hedge : il **exige** donc `WithRetry`
ou `WithHedge` — configuré sans aucun des deux, `NewPolicy` panique avec
`ErrTimeBudgetWithoutConsumer`. Observabilité : le hook `OnTimeBudgetExceeded` et
la métrique `TimeBudgetExceeded`. Voir [`examples/22-time-budget`](examples/22-time-budget).

### Propagation de deadline dure

Par défaut le budget laisse `context.Context.Deadline()` **non défini** : un
callee gRPC/HTTP en aval ne peut donc pas le voir ni shed early. Passez
`PropagateDeadline` pour exposer en plus le budget comme une **deadline dure,
pilotée par l'horloge** :

```go
policy := r8e.NewPolicy[Response]("svc",
    r8e.WithRetry(5, r8e.ExponentialBackoff(100*time.Millisecond)),
    r8e.WithTimeBudget(350*time.Millisecond, r8e.PropagateDeadline()),
)
```

Chaque tentative s'exécute alors sous un contexte dont `Deadline()` rapporte
l'instant du budget (le client en aval en dérive son propre timeout réseau) et
dont l'annulation **annule une tentative en cours** quand le budget expire — en
remontant le même `ErrTimeBudgetExceeded` (enveloppant `context.DeadlineExceeded`)
que le chemin coopératif. La deadline est pilotée par le `Clock` de la policy,
pas par l'horloge murale, donc elle reste déterministe sous une fake clock en
test ; comme une vraie deadline de contexte est intrinsèquement wall-clock, la
*valeur propagée* n'a de sens pour de vrais callees que sur `RealClock`
(production). Exprimable en config via `propagate_deadline` (exige `time_budget`,
sinon `ErrDeadlinePropagationWithoutBudget`) et rechargeable à chaud via
`Reconfigure`. Voir [`examples/28-deadline-propagation`](examples/28-deadline-propagation).

## Retry Budget

Un retry budget plafonne le nombre de retries relativement au taux d'échec, pour
qu'une dépendance en difficulté ne soit pas ensevelie sous une *retry storm*. Il
suit le modèle `retryThrottling` de gRPC : un token bucket adaptatif (capacité
`MaxTokens`) où chaque succès rend `TokenRatio` tokens et chaque échec réessayable
en retire un. Tant que le bucket est à la moitié de sa capacité ou en dessous,
les retries sont supprimés — l'appel renvoie l'erreur réelle du downstream, et la
tentative initiale de chaque requête passe toujours.

```go
policy := r8e.NewPolicy[string]("svc",
    r8e.WithRetry(5, r8e.ExponentialBackoff(50*time.Millisecond)),
    r8e.WithRetryBudget(r8e.MaxTokens(10), r8e.TokenRatio(0.1)), // défauts gRPC
)
```

Pour coordonner les retries entre plusieurs policies d'un même process,
construisez un budget et partagez-le :

```go
budget := r8e.NewRetryBudget(r8e.MaxTokens(10), r8e.TokenRatio(0.1))

a := r8e.NewPolicy[string]("a", r8e.WithRetry(3, strategy), r8e.WithSharedRetryBudget(budget))
b := r8e.NewPolicy[string]("b", r8e.WithRetry(3, strategy), r8e.WithSharedRetryBudget(budget))
```

Un budget exige `WithRetry` — en configurer un sans pattern retry panique dans
`NewPolicy` (ou, en construction par config, `BuildOptions` renvoie
`ErrRetryBudgetWithoutRetry`). Le throttling est observable via le hook
`OnRetryBudgetExceeded`, les métriques `RetryBudgetExceeded` /
`RetryBudgetTokens`, et une condition de santé dégradée `retry_budget_exhausted`
(elle ne bloque jamais la readiness — les tentatives initiales passent toujours).
Un budget *partagé* reporte le même niveau de tokens et la même condition sous le
nom de chaque policy qui le partage : agrégez sa jauge avec `max`/`avg`, pas
`sum`. Voir [`examples/19-retry-budget`](examples/19-retry-budget).

## Budget de concurrence

Un budget de concurrence est le complément *en dimension concurrence* du retry
budget : là où celui-ci throttle le **débit** des retries dans le temps, celui-ci
plafonne combien de retries et de hedges peuvent être **en vol simultanément**.
Sous une rafale d'échecs, de nombreux appelants réessaient ensemble et multiplient
la charge sur une dépendance en difficulté — le budget n'en admet qu'une part
bornée et déleste le reste.

Un retry ou un hedge n'est autorisé que tant que

```
concurrent < max(MinConcurrency, MaxRatio × exécutions en vol)
```

Le terme `MaxRatio` met le plafond à l'échelle du trafic courant (un service chargé
tolère plus de retries concurrents qu'un service au repos) et le plancher
`MinConcurrency` empêche un service à faible trafic de ne plus pouvoir réessayer du
tout. Cela reproduit l'execution budget de failsafe-go ; les défauts (`MaxRatio`
0.25, `MinConcurrency` 5) lui correspondent.

```go
policy := r8e.NewPolicy[string]("svc",
    r8e.WithRetry(5, r8e.ExponentialBackoff(50*time.Millisecond)),
    r8e.WithConcurrencyBudget(r8e.MaxRatio(0.25), r8e.MinConcurrency(5)),
)
```

La première tentative de chaque appel est la baseline et n'est jamais filtrée ;
seuls les retries (tentatives 2 et suivantes) et la seconde tentative concurrente
du hedge prennent un permis. Quand le budget est épuisé, un retry est supprimé et
l'appel échoue avec `ErrConcurrencyBudgetExceeded` (encapsulant la dernière erreur
downstream) ; un hedge hors budget n'est simplement pas lancé (le primaire tourne
toujours). Il se compose avec le retry budget — utilisez les deux pour borner les
retries sur les *deux* axes — et un même budget peut être partagé entre policies
pour un plafond à l'échelle du process :

```go
budget := r8e.NewConcurrencyBudget(r8e.MaxRatio(0.25), r8e.MinConcurrency(5))

a := r8e.NewPolicy[string]("a", r8e.WithRetry(3, strategy), r8e.WithSharedConcurrencyBudget(budget))
b := r8e.NewPolicy[string]("b", r8e.WithHedge(20*time.Millisecond), r8e.WithSharedConcurrencyBudget(budget))
```

Un budget exige `WithRetry` ou `WithHedge` — en configurer un sans aucun des deux
panique dans `NewPolicy` (ou `BuildOptions` renvoie
`ErrConcurrencyBudgetWithoutConsumer`). Le délestage est observable via le hook
`OnConcurrencyBudgetExceeded`, les métriques `ConcurrencyBudgetExceeded` /
`ConcurrencyBudgetInUse`, et une condition de santé dégradée
`concurrency_budget_exhausted` (elle ne bloque jamais la readiness — les tentatives
initiales passent toujours). Voir
[`examples/33-concurrency-budget`](examples/33-concurrency-budget).

## Cache read-through

`WithCache` mémoïse les résultats réussis dans la chaîne. Un hit frais retourne la
valeur en cache et court-circuite toute la policy ; un miss exécute la chaîne et
met en cache un résultat réussi pour la durée du TTL. La clé provient du contexte
de l'appel via une fonction de clé — le même idiome que le [Coalescing de
requêtes](#coalescing-de-requêtes), donc une seule fonction de clé peut piloter les
deux. Retourner une clé vide exclut l'appel de la mise en cache.

```go
cache := otter.MustNew[string, r8e.CacheEntry[string]](r8e.CacheConfig{MaxSize: 10_000})

policy := r8e.NewPolicy[string]("catalog",
    r8e.WithCache(cache, keyFromCtx, 30*time.Second,
        r8e.StaleIfError(5*time.Minute),     // sert le périmé en cas d'erreur après le TTL
        r8e.NegativeCache(2*time.Second),    // met aussi brièvement les échecs en cache
        r8e.RefreshAhead(25*time.Second),    // recharge les clés chaudes avant expiration
    ),
    r8e.WithCoalesce(keyFromCtx),            // fusionne la rafale de miss
    r8e.WithTimeout(time.Second),
)
```

Le `Cache` sous-jacent est paramétré par `CacheEntry[T]` (le wrapper que r8e stocke
pour porter l'âge de chaque entrée et toute erreur enregistrée) ; construisez donc
l'adaptateur avec `r8e.CacheEntry[T]` comme type de valeur. La fraîcheur est mesurée
contre le `Clock` de la policy, pas contre l'expiration propre du cache : elle reste
déterministe sous une horloge factice.

Il unifie quatre comportements derrière une seule option :

- **Read-through** — dans le TTL frais, un hit saute entièrement l'aval.
- **Refresh-ahead** (`RefreshAhead`) — un hit qui tombe en fin de fenêtre fraîche
  (au-delà du seuil de rafraîchissement mais encore frais) est servi immédiatement
  et déclenche en plus un unique rechargement de fond coalescé, de sorte qu'une clé
  chaude continue de servir des hits frais au lieu de retomber dans un miss
  synchrone à l'expiration (`refreshAfterWrite` de Caffeine). Le rechargement est
  détaché (l'appelant n'est pas bloqué) et dédoublonné par clé ; un rechargement en
  échec est best-effort (l'entrée courante est conservée, la prochaine lecture en
  fenêtre réessaie), un succès déclenche `OnCacheRefreshed`. Comme le rechargement
  détaché perd la deadline de l'appelant, une policy dont le seuil se déclenche
  réellement doit aussi avoir un `WithTimeout` pour le borner (sinon
  `ErrRefreshAheadWithoutTimeout`) ; en usage autonome, bornez le loader vous-même.
  Mettez le seuil plus court que le TTL frais ; au-delà ou égal, le refresh-ahead
  est inerte (et n'exige aucun timeout).
- **Stale-if-error** (`StaleIfError`) — après le TTL frais, une valeur subsiste
  comme repli périmé pendant la durée donnée. Un appel dans la fenêtre périmée
  ré-exécute pour rafraîchir, mais si cela échoue la valeur périmée est servie au
  lieu de l'erreur (RFC 5861 stale-if-error), déclenchant `OnStaleServed`. Cela
  englobe le [Stale Cache](#stale-cache) autonome pour l'usage en chaîne.
- **Negative caching** (`NegativeCache`) — un échec sans valeur périmée de repli
  est lui-même mis en cache pour un court TTL, donc les appels répétés vers une clé
  connue défaillante échouent vite avec l'erreur enregistrée au lieu de marteler
  l'aval.

`ForceRefresh(ctx)` retourne un contexte enfant qui fait qu'un appel contourne la
lecture en cache et repeuple en cas de succès. Trois erreurs de configuration
paniquent dans `NewPolicy` : une fonction de clé nil (`ErrCacheNilKeyFunc`), un
cache nil (`ErrCacheNilCache`) et un TTL non positif (`ErrCacheNonPositiveTTL`).
Comme le cache et la fonction de clé sont du code, la mise en cache est code-only —
elle est délibérément absente de `PolicyConfig`, `BuildOptions` et `Reconfigure`,
exactement comme le coalescing.

Observabilité : les hooks `OnCacheHit` / `OnCacheMiss` / `OnCacheStored` /
`OnStaleServed` / `OnCacheRefreshed` et les compteurs `CacheHits` / `CacheMisses` /
`CacheStores` / `CacheStaleServed` / `CacheRefreshes` (hits/(hits+misses) est le
taux de hit). La mise en cache est une optimisation saine, donc sans condition de
santé — uniquement des métriques. Un `ReadThroughCache` peut aussi s'utiliser seul
via `r8e.NewReadThroughCache` (configurez l'horloge et les hooks avec `CacheClock` /
`CacheHooks`). Voir [`examples/24-read-through-cache`](examples/24-read-through-cache)
et [`examples/38-cache-refresh-ahead`](examples/38-cache-refresh-ahead).

## Coalescing de requêtes

Le coalescing de requêtes (alias *singleflight*) fusionne les appels concurrents
qui partagent une clé en une seule exécution partagée : le premier appelant (le
*leader*) exécute le travail, et chaque appelant qui arrive pendant qu'il est en
cours (un *follower*) attend et partage ce résultat unique. Quand une clé de
cache chaude expire, N cache miss simultanés deviennent un seul appel downstream
au lieu de N — la solution classique au *cache stampede*.

```go
policy := r8e.NewPolicy[string]("user-fetch",
    r8e.WithTimeout(time.Second),       // requis — borne l'appel partagé
    r8e.WithCoalesce(func(ctx context.Context) string {
        return "user:" + userIDFrom(ctx) // dérive la clé depuis le contexte d'appel
    }),
)
```

La fonction de clé lit le contexte de l'appel : estampillez l'identité de la
requête dans `ctx` en amont et relisez-la ici. Renvoyer une chaîne vide exclut
l'appel du coalescing (il s'exécute seul). Deux erreurs de configuration paniquent
dans `NewPolicy` : une fonction de clé nil (`ErrCoalesceNilKeyFunc`) et une policy
sans `WithTimeout` pour borner l'appel partagé détaché (`ErrCoalesceWithoutTimeout`).

Le coalescing ne déduplique que les appels qui se chevauchent dans le temps ;
une fois le leader terminé, sa clé est libérée, donc un appel ultérieur repart de
zéro. Ce n'est **pas** un cache — placez-en un devant ou derrière pour cela.

**Contexte partagé détaché.** L'appel partagé s'exécute sous un contexte détaché
de tout appelant (`context.WithoutCancel`) : l'annulation par un seul appelant ne
peut pas avorter le travail dont dépend tout le groupe, et le travail va jusqu'au
bout même si tous les appelants partent (utile pour quand même remplir un cache).
Chaque appelant — leader compris — cesse d'attendre dès que *son propre* contexte
est terminé et renvoie `ctx.Err()`, donc un leader lent ne bloque jamais un
follower au-delà de sa deadline. Le détachement retire aussi la deadline de
l'appelant — c'est pourquoi le coalescing **exige** un `WithTimeout` pour borner
le travail partagé ; sans lui, un leader dont le `fn` ne retourne jamais
parquerait une goroutine et figerait sa clé.

Observabilité : les hooks `OnCoalesceLeader` / `OnCoalesceFollower`, les
compteurs `CoalesceLeaders` / `CoalesceFollowers` (leur ratio est le taux de
déduplication) et la jauge `CoalesceInFlight`. Le coalescing est une optimisation
saine : il n'expose aucune condition de santé. Voir
[`examples/20-coalesce`](examples/20-coalesce).

Un `Coalescer` peut aussi s'utiliser seul, sans `Policy` (pas de timeout de policy
ici, donnez donc sa propre deadline à `fetch`) :

```go
c := r8e.NewCoalescer[string](&r8e.Hooks{})
val, err := c.Do(ctx, "user:42", fetch)
```

## Concurrence adaptative

`WithAdaptiveConcurrency` remplace le plafond fixe d'un [Bulkhead](#bulkhead) par
une limite que la policy **ajuste elle-même depuis la latence observée**, via
l'algorithme Gradient2 de Netflix. Chaque appel terminé échantillonne sa RTT ;
quand la RTT courante monte au-dessus d'une baseline long-terme lissée — la
signature d'une file qui se forme en aval — la limite baisse, et quand la latence
est stable la limite remonte. Les appels arrivant alors que l'in-flight est à la
limite courante sont rejetés avec `ErrConcurrencyLimited`.

```go
policy := r8e.NewPolicy[Response]("downstream",
    r8e.WithAdaptiveConcurrency(
        r8e.InitialLimit(20),   // point de départ avant toute mesure
        r8e.MinLimit(1),        // jamais moins que ça
        r8e.MaxLimit(200),      // jamais plus que ça
        r8e.RTTTolerance(1.5),  // tolère une hausse de RTT de 1.5x avant de baisser
    ),
)
```

Elle occupe le même slot que le bulkhead : elle est donc **mutuellement
exclusive** avec `WithBulkhead`. Configurer les deux panique `NewPolicy` avec
`ErrConcurrencyLimiterConflict` (ou, en construction par config, `BuildOptions`
le renvoie). La limite ne grandit que lorsque le limiter est réellement chargé
(in-flight à au moins la moitié de la limite), donc un service au repos n'est
jamais poussé à sonder plus haut.

Observabilité : les hooks `OnConcurrencyRejected` et
`OnConcurrencyLimitChanged(limit)`, le compteur `ConcurrencyRejected` et les
jauges `ConcurrencyLimit` / `ConcurrencyInFlight`. La saturation s'expose comme
une condition de santé dégradée `concurrency_limited` (elle ne bloque jamais la
readiness). Voir
[`examples/21-adaptive-concurrency`](examples/21-adaptive-concurrency).

Un `AdaptiveLimiter` s'utilise aussi seul via `NewAdaptiveLimiter`, `Acquire` et
`Record`.

## Throttle adaptatif

`WithAdaptiveThrottle` ajoute le **throttling adaptatif côté client** de Google
SRE : un délesteur de charge probabiliste qui rejette les appels localement —
avant qu'ils n'atteignent un backend en difficulté — proportionnellement à la
fréquence à laquelle ce backend les rejette déjà. Il maintient une fenêtre
glissante des requêtes tentées contre les requêtes acceptées par le backend et,
dès que les requêtes dépassent `OverloadRatio` (K) fois les accepts, déleste les
nouveaux appels avec la probabilité SRE `max(0, (requests − K·accepts) /
(requests + 1))`. Un appel délesté retourne `ErrThrottled` sans exécuter aucun
étage interne.

```go
policy := r8e.NewPolicy[Response]("downstream",
    r8e.WithAdaptiveThrottle(
        r8e.OverloadRatio(2),               // K : tolère un écart requests/accepts de 2x
        r8e.MaxRejectionRate(0.9),          // laisse toujours passer ≥10% pour sonder
        r8e.ThrottleWindow(10*time.Second), // longueur de la fenêtre glissante
        r8e.MinRequests(10),                // un minimum de trafic avant de délester
    ),
)
```

Contrairement au [Circuit Breaker](#circuit-breaker) binaire, le throttle amortit
la charge **graduellement et proportionnellement**, et se place juste à
l'extérieur du breaker dans la chaîne — idéalement pour ramener un backend en
récupération vers la santé avant même que le breaker ne s'ouvre. La probabilité
est plafonnée par `MaxRejectionRate` (défaut `0.9`) afin qu'une fraction du trafic
sonde toujours la récupération, et le throttle récupère seul à mesure que les
échecs sortent de la fenêtre. Un appel délesté localement n'atteint jamais le
breaker, donc il ne compte pas contre lui.

Par défaut, toute erreur de la chaîne interne compte comme un rejet du backend ;
restreignez cela avec `ThrottleClassifier(func(error) bool)` pour ne compter que
les vraies erreurs de surcharge (un 404 ou une erreur de validation est alors
traité comme un accept). Les paramètres numériques sont configurables en JSON
(`AdaptiveThrottleConfig`) et rechargeables à chaud ; le classifieur est en code
seulement.

Observabilité : le hook `OnThrottled`, le compteur `Throttled` et la jauge
`ThrottleProbability`. Le délestage s'expose comme une condition de santé dégradée
`throttling` (elle ne bloque jamais la readiness). Un `Throttler` s'utilise aussi
seul via `NewThrottler`, `Allow` et `Record`. Voir
[`examples/25-adaptive-throttle`](examples/25-adaptive-throttle).

### Sheddabilité des requêtes

Marquez un contexte pour contrôler comment le throttler traite un appel spécifique :

```go
// Appel critique — toujours admis, même à la charge maximale.
ctx := r8e.WithSheddability(ctx, r8e.SheddabilityNever)
result, err := policy.Do(ctx, fn)

// Tâche de fond — délestée en premier dès qu'un délestage est actif.
ctx := r8e.WithSheddability(ctx, r8e.SheddabilityAlways)
result, err := policy.Do(ctx, fn)
```

Les trois niveaux sont : `SheddabilityNever` (bypass — trafic critique),
`SheddabilityDefault` (valeur zéro — probabilité SRE normale) et
`SheddabilityAlways` (délestage prioritaire — travail en arrière-plan ou
spéculatif). Le throttler adaptatif et le
[gouverneur de burn-rate SLO](#gouverneur-de-burn-rate-slo) lisent tous deux
l'annotation ; les autres patterns ne sont pas affectés. Voir
[`examples/29-sheddability`](examples/29-sheddability).

## Gouverneur de burn-rate SLO

`WithSLO(target)` ajoute un **délesteur piloté par le burn-rate de l'error budget
d'un SLO** : là où le throttle adaptatif déleste selon le ratio accepts/requests
courant du backend, le gouverneur déleste selon la vitesse à laquelle le service
consomme l'error budget d'un objectif *déclaré*. Un taux de succès cible (ex.
`0.999`) implique un error budget de `1 − target` ; le **burn rate** est le taux
d'erreur servi observé divisé par ce budget — `1` consomme le budget exactement au
rythme soutenable, `14.4` est le seuil « fast burn » de Google SRE. Un appel
délesté renvoie `ErrSLOShed` sans exécuter aucune étape interne.

```go
policy := r8e.NewPolicy[Response]("checkout",
    r8e.WithSLO(0.999,                          // objectif 99,9% → budget d'erreur 0,1%
        r8e.SLOLongWindow(time.Minute),         // fenêtre longue, stable (burn soutenu)
        r8e.SLOShortWindow(5*time.Second),      // fenêtre courte, réactive (burn courant)
        r8e.BurnThreshold(2.0),                 // déleste dès >2x le rythme soutenable
        r8e.MaxShedRate(0.9),                   // laisse toujours ≥10% passer pour sonder
        r8e.SLOMinRequests(20),                 // un minimum de trafic avant tout délestage
    ),
)
```

**Burn rate multi-fenêtre (Google SRE).** Le gouverneur mesure le burn rate sur
deux fenêtres glissantes — une courte et réactive, une longue et stable — et ne
déleste que lorsque les **deux** dépassent `BurnThreshold`. La longue détecte un
burn soutenu ; la courte confirme qu'il est toujours en cours, donc le délestage
s'engage vite sur un vrai burn mais ignore un pic bref visible dans une seule
fenêtre, et se désengage rapidement quand le burn cesse.

**Délestage proportionnel et sheddability-aware.** Une fois engagé, un appel est
délesté avec une probabilité `max(0, 1 − BurnThreshold/burnRate)` plafonnée à
`MaxShedRate`, à l'échelle du burn rate de la fenêtre courte. La probabilité est
appliquée via la [sheddabilité](#sheddabilité-des-requêtes) de l'appel :
`SheddabilityNever` est toujours admis, `SheddabilityAlways` est délesté dès qu'un
délestage est actif, et `SheddabilityDefault` est délesté avec la probabilité — le
budget restant est ainsi dépensé sur les appels qui comptent. Un appel délesté
localement n'est **jamais enregistré**, donc délester le trafic sheddable ne
consomme pas lui-même de budget.

Il se place juste à l'extérieur du throttle adaptatif dans la chaîne (l'objectif
SLO est le souci de plus haut niveau). Par défaut toute erreur non-nil consomme du
budget ; restreignez via `SLOClassifier(func(error) bool)` pour ne compter que les
vraies erreurs de l'objectif (un 404 ou une erreur de validation est alors traité
comme un succès servi). Une cible hors plage est ramenée à une valeur par défaut
plutôt que rejetée. Les paramètres numériques sont configurables en JSON
(`SLOConfig`, avec le `target` requis) et rechargeables à chaud ; le classifier est
code-only.

Observabilité : le hook `OnSLOShed`, le compteur `SLOShed`, et les gauges
`SLOBurnRate` et `SLOShedProbability`. Le délestage se traduit par une condition de
santé dégradée `slo_burning` (qui ne bloque jamais la readiness). Un `SLOGovernor`
peut aussi être utilisé seul avec `NewSLOGovernor`, `Allow` et `Record`. Voir
[`examples/40-slo-governor`](examples/40-slo-governor).

## Récupération de panic (panic → error)

`WithRecover` enveloppe l'appel le plus interne et convertit tout panic en
valeur `*PanicError` au lieu de le propager dans la pile d'appels. L'erreur
récupérée contient à la fois la valeur originale du panic et la trace de pile
de la goroutine capturée au moment de la récupération.

```go
policy := r8e.NewPolicy[string]("svc",
    r8e.WithRecover(),
    r8e.WithRetry(3, r8e.ConstantBackoff(0)),  // réessayer l'appel qui a paniché
    r8e.WithFallback("default"),               // ou basculer sur fallback
    r8e.WithHooks(&r8e.Hooks{
        OnPanic: func(value any) { log.Printf("panic récupéré : %v", value) },
    }),
)

_, err := policy.Do(ctx, fn)
if errors.Is(err, r8e.ErrPanic) {
    var pe *r8e.PanicError
    errors.As(err, &pe)
    log.Printf("value=%v\nstack=%s", pe.Value, pe.Stack)
}
```

`WithRecover` se positionne **le plus à l'intérieur** de la chaîne (à l'intérieur
du fork hedge), de sorte que chaque goroutine hedge possède son propre wrapper de
récupération et que retry voit l'erreur récupérée. Le hook `OnPanic` se déclenche
pour chaque panic intercepté. Le compteur `PanicsRecovered` s'incrémente
automatiquement. Usage autonome : `r8e.DoRecover[T](ctx, fn, hooks)`.
Voir [`examples/31-recover`](examples/31-recover).

## Injection de chaos

`WithChaos` perturbe délibérément l'appel pour éprouver les patterns de résilience
**de la policy elle-même** — est-ce que mon retry rattrape la faute injectée ?
est-ce que mon timeout rattrape la latence injectée ? C'est la déclinaison r8e du
chaos engineering de Polly v8 / Simmy, avec quatre stratégies injectant chacune
indépendamment sur une fraction des appels :

- **`ChaosFault(prob, err)`** — échoue l'appel avec `err` (défaut : `ErrChaosInjected`).
- **`ChaosLatency(prob, d)`** — retarde l'appel de `d` sur le `Clock` de la policy, puis continue.
- **`ChaosOutcome(prob, fn)`** — court-circuite avec un faux résultat typé ou une erreur.
- **`ChaosBehavior(prob, fn)`** — exécute un effet de bord avant l'appel, puis continue.

```go
policy := r8e.NewPolicy[string]("svc",
    r8e.WithTimeout(100*time.Millisecond),
    r8e.WithRetry(4, r8e.ConstantBackoff(time.Millisecond)),
    r8e.WithFallback("default"),
    r8e.WithChaos(
        // 30% des appels canary échouent — le retry l'absorbe-t-il ?
        r8e.ChaosFault(0.3, errors.New("injected"), r8e.ChaosEnabled(isCanary)),
        // 10% traînent au-delà du timeout — le timeout le rattrape-t-il ?
        r8e.ChaosLatency(0.1, 250*time.Millisecond, r8e.ChaosEnabled(isCanary)),
    ),
)
```

Le chaos se positionne **le plus à l'intérieur** de la chaîne — une dépendance
défaillante simulée — de sorte que tous les autres patterns l'enveloppent et y
réagissent : un retry re-tire chaque stratégie à chaque tentative, un timeout
borne la latence injectée et un `WithRecover` rattrape un panic levé par un chaos
behavior. Les stratégies s'exécutent dans l'ordre donné, et une faute ou un
outcome court-circuite le reste : placez une faute **avant** une latence pour
éviter l'attente de la latence quand la faute se déclenche (l'ordre recommandé
par Polly).

Gatez n'importe quelle stratégie par appel avec `ChaosEnabled(func(ctx) bool)`
pour un chaos canary sûr en production : lisez un feature flag ou un en-tête de
requête dans le contexte et renvoyez si cet appel est soumis au chaos — coupant
le chaos à l'exécution sans redéploiement. Comme les fonctions outcome/behavior
et le prédicat `ChaosEnabled` sont du code, le chaos est code-only : il est
délibérément absent de `PolicyConfig`, `BuildOptions` et `Reconfigure`, comme
`WithCoalesce` et `WithCache`. La latence est mesurée sur le `Clock` injecté,
donc le chaos est déterministe en test. Observabilité : le hook `OnChaosInjected`
(avec le type de stratégie) et le compteur `ChaosInjected`, exporté en compteur
OpenTelemetry `r8e.policy.chaos_injected`. Voir
[`examples/37-chaos-injection`](examples/37-chaos-injection).

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

Hooks disponibles sur `Hooks` (34) : `OnRetry`, `OnCircuitOpen`, `OnCircuitClose`, `OnCircuitHalfOpen`, `OnCircuitRamping`, `OnSlowCallRateExceeded`, `OnRateLimited`, `OnRateAdapted`, `OnBulkheadFull`, `OnBulkheadAcquired`, `OnBulkheadReleased`, `OnBulkheadQueued`, `OnBulkheadTimeout`, `OnCoDelShed`, `OnTimeout`, `OnHedgeTriggered`, `OnHedgeWon`, `OnFallbackUsed`, `OnRetryBudgetExceeded`, `OnTimeBudgetExceeded`, `OnCoalesceLeader`, `OnCoalesceFollower`, `OnConcurrencyRejected`, `OnConcurrencyLimitChanged`, `OnThrottled`, `OnSLOShed`, `OnCacheHit`, `OnCacheMiss`, `OnCacheStored`, `OnStaleServed`, `OnCacheRefreshed`, `OnPanic`, `OnConcurrencyBudgetExceeded`, `OnChaosInjected`.

StaleCache a ses propres hooks configurés via `StaleCacheOption` : `OnStaleServed[K,V]` et `OnCacheRefreshed[K,V]` (voir [Stale Cache](#stale-cache)).

### Métriques

Au-delà des callbacks, chaque policy tient des compteurs cumulés et des gauges live — pas besoin de câbler des hooks à la main. `Policy.Metrics()` renvoie un instantané, et `Registry.Snapshot()` un par policy enregistrée :

```go
m := policy.Metrics()
fmt.Println(m.Retries, m.CircuitOpens, m.FallbacksUsed) // compteurs
fmt.Println(m.CircuitState, m.BulkheadInUse, m.Saturated) // gauges live
```

**Percentiles de latence.** Chaque policy enregistre aussi la durée bout-en-bout de chaque appel `Do()` dans un histogramme à fenêtre glissante et expose les **p50/p95/p99** récents — aucune option à activer, la même instrumentation toujours active que resilience4j offre sur ses timers. Les percentiles révèlent une queue lente qu'une moyenne masque :

```go
m := policy.Metrics()
fmt.Println(m.LatencyP50, m.LatencyP95, m.LatencyP99) // fenêtre récente (~10s)
fmt.Println(m.LatencySamples)                          // 0 ⇒ percentiles pas encore significatifs
```

La fenêtre est un [DDSketch](https://arxiv.org/abs/1908.10693) : les percentiles restent à ~2 % d'erreur relative, la vieille latence vieillit hors fenêtre, et la mesure se fait sur le `Clock` de la policy — donc déterministe en test. Tous les appels comptent — succès, échecs et rejets fast-fail — si bien qu'en surcharge les percentiles bas baissent à mesure que les rejets instantanés entrent dans la fenêtre. Voir [`examples/34-latency-percentiles`](examples/34-latency-percentiles). Le pont OpenTelemetry ci-dessous les publie comme gauges `r8e.policy.latency_p50/p95/p99` (en secondes).

Deux ponts sans configuration les exposent :

```go
// Endpoint JSON de debug (stdlib uniquement).
http.Handle("/metrics", r8ehttp.MetricsHandler(r8e.DefaultRegistry()))

// OpenTelemetry métriques — compteurs + gauges observables par policy, étiquetés par nom.
// Dans le module séparé r8eotel pour garder le cœur sans dépendance.
_, err := r8eotel.Register(meter, r8e.DefaultRegistry())

// Traces OpenTelemetry — span root par appel Do() + span enfant par invocation fn.
// Les chaînes de retry et les races de hedge apparaissent comme enfants dans Jaeger/Tempo.
traced := r8eotel.Trace(policy, otel.GetTracerProvider())
```

Voir [`r8eotel/README.fr.md`](r8eotel/README.fr.md) pour la documentation complète du pont OpenTelemetry et ses exemples.

## Hot reload

Réglez les paramètres des patterns qu'une policy possède déjà — à l'exécution, sans redéploiement. `Policy.Reconfigure` applique chaque champ non-nil d'un `PolicyConfig` au pattern live ; les champs nil sont laissés inchangés :

```go
err := policy.Reconfigure(r8e.PolicyConfig{
    CircuitBreaker: &r8e.CircuitBreakerConfig{FailureThreshold: ptr(3)},
    RateLimit:      ptr(50.0),
})
```

Pilotez-le depuis un fichier via `r8econf`, qui relit, revalide et reconfigure chaque policy déjà construite :

```go
store, _ := r8econf.Load("config.json")
// ... GetPolicy(...) construit des policies qui s'auto-enregistrent ...
err := store.Reload("config.json") // ex. sur SIGHUP ou changement de ConfigMap
```

Le hot-reload **règle** les patterns existants ; il ne peut **ni ajouter ni retirer** un pattern (la chaîne de middlewares est figée). Configurer un pattern absent renvoie `ErrPatternAbsent` — reconstruisez via `GetPolicy`/`NewPolicy` pour un changement structurel. `Registry.Reconfigure(name, cfg)` cible une seule policy enregistrée.

## Santé et readiness

Les policies remontent leur état de santé, et le registre peut l'exposer en HTTP.

> **La readiness est opt-in.** Par **défaut**, la santé d'une policy **n'affecte pas** la sonde de readiness — un circuit breaker ouvert est remonté comme unhealthy mais **ne retire pas** le pod de la rotation. C'est délibéré : sinon, une dépendance partagée qui se dégrade ouvrirait le breaker sur **tous** les réplicas en même temps et ferait retirer toute la flotte par Kubernetes, transformant un hoquet de dépendance en panne totale. Ne gatez la readiness que sur une dépendance sans laquelle le pod ne peut pas servir, avec `WithReadinessImpact()`. Utilisez `failureThreshold`/`periodSeconds` de la sonde pour l'hystérésis.

```go
import "net/http"

apiPolicy := r8e.NewPolicy[string]("api-gateway",
    r8e.WithCircuitBreaker(),
)
dbPolicy := r8e.NewPolicy[string]("database",
    r8e.WithCircuitBreaker(),
    r8e.WithReadinessImpact(), // celle-ci gate /readyz quand son breaker s'ouvre
)

// /readyz gate le trafic (503 si une policy readiness-impacting est critique).
http.Handle("/readyz", r8ehttp.ReadinessHandler(r8e.DefaultRegistry()))
// /healthz est informationnel — santé complète par policy, toujours 200, jamais de gate.
http.Handle("/healthz", r8ehttp.HealthHandler(r8e.DefaultRegistry()))
```

Vérifier la santé par programmation :

```go
status := apiPolicy.HealthStatus()
fmt.Println(status.Healthy)     // true/false
fmt.Println(status.Conditions)  // toutes les conditions actives, ex. ["rate_limited","bulkhead_full"]
fmt.Println(status.State)       // résumé déterministe le plus sévère : "circuit_open", "healthy", …
fmt.Println(status.Criticality) // CriticalityNone, CriticalityDegraded, CriticalityCritical

report := r8e.DefaultRegistry().Health() // agrégat : "healthy" | "degraded" | "unhealthy"
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

Le chargement de config depuis un fichier vit dans le package edge `r8econf`,
afin que le package principal reste sans dépendance :

```go
store, err := r8econf.Load("config.json")
if err != nil {
    log.Fatal(err)
}

// Obtenir une policy typée — les options de config sont fusionnées avec les options en code
policy, err := r8econf.GetPolicy[string](store, "payment-api",
    r8e.WithFallback("service indisponible"),
)
if err != nil {
    log.Fatal(err)
}
```

Stratégies de backoff supportées en config : `"constant"`, `"exponential"`, `"linear"`, `"exponential_jitter"`.

Les backends de cache peuvent être configurés séparément via `r8econf.LoadCacheConfig` :

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
cfg, err := r8econf.LoadCacheConfig("caches.json", "pricing")
if err != nil {
    log.Fatal(err)
}
cache := otteradapter.MustNew[string, string](cfg)
sc := r8e.NewStaleCache(cache, cfg.TTL)
```

## Configuration personnalisée

Les structs exportées `PolicyConfig`, `CircuitBreakerConfig` et `RetryConfig` portent des tags `json` et `yaml`, vous pouvez donc les embarquer dans votre propre config applicative et désérialiser depuis n'importe quel format. Appelez `r8e.BuildOptions` pour convertir une `PolicyConfig` en options fonctionnelles sans passer par `r8econf.Load`.

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

## Skill Claude Code

r8e inclut un fichier skill [Claude Code](https://docs.anthropic.com/en/docs/claude-code) documentant l'API de r8e, ses patterns et ses idiomes pour l'assistant. Pour l'activer, creez un lien symbolique ou copiez le skill dans le repertoire `.claude/skills/` de votre projet :

```bash
mkdir -p .claude/skills
cp -r ./vendor/github.com/byte4ever/r8e/claude-skill .claude/skills/r8e
```

Ou si vous avez clone r8e directement :

```bash
mkdir -p .claude/skills
ln -s "$(go list -m -f '{{.Dir}}' github.com/byte4ever/r8e)/claude-skill" .claude/skills/r8e
```

Une fois installe, Claude Code appliquera automatiquement ses connaissances r8e lorsque vous travaillez sur du code lie a la resilience.

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
go run ./examples/17-httpx-basic/
go run ./examples/18-httpx-retry/
go run ./examples/19-retry-budget/
go run ./examples/20-coalesce/
go run ./examples/21-adaptive-concurrency/
go run ./examples/22-time-budget/
go run ./examples/23-retry-after/
go run ./examples/24-read-through-cache/
go run ./examples/25-adaptive-throttle/
go run ./examples/26-slow-call-breaker/
go run ./examples/27-bulkhead-wait/
go run ./examples/28-deadline-propagation/
go run ./examples/29-sheddability/
go run ./examples/30-recovery-backoff/
go run ./examples/31-recover/
go run ./examples/32-aimd-rate-limit/
go run ./examples/33-concurrency-budget/
go run ./examples/34-latency-percentiles/
go run ./examples/35-adaptive-timeout/
go run ./examples/36-adaptive-hedge/
go run ./examples/37-chaos-injection/
go run ./examples/38-cache-refresh-ahead/
go run ./examples/39-ramp-recovery/
go run ./examples/40-slo-governor/
go run ./examples/41-codel-queue/
```

## Licence

MIT
