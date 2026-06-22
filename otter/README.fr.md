# r8e/otter

[English](README.md) · [Français]

Adaptateur de cache Otter pour [**r8e**](../README.fr.md). Il implémente
l'interface `r8e.Cache` au-dessus d'[Otter](https://github.com/maypok86/otter) —
un cache haute performance, sans contention, avec TTL par entrée — afin
d'alimenter la politique de cache read-through de r8e (`r8e.WithCache` /
`r8e.ReadThroughCache`) ou le `r8e.StaleCache` autonome avec un cache de qualité
production.

## Installation

```bash
go get github.com/byte4ever/r8e/otter
```

## Démarrage rapide

```go
import (
    "github.com/byte4ever/r8e"
    otteradapter "github.com/byte4ever/r8e/otter"
)

// Le cache stocke des r8e.CacheEntry[V] (l'enveloppe de fraîcheur), avec une clé
// string — le même type de clé que r8e.WithCache.
cache := otteradapter.MustNew[string, r8e.CacheEntry[string]](
    r8e.CacheConfig{MaxSize: 10_000},
)

policy := r8e.NewPolicy[string]("profiles",
    r8e.WithCache(
        cache,
        func(ctx context.Context) string { return userIDFromCtx(ctx) },
        30*time.Second, // TTL de fraîcheur
    ),
)
```

Le premier appel pour une clé exécute le downstream et stocke le résultat ; les
appels dans la fenêtre de fraîcheur sont servis depuis Otter sans réexécution.
Voir [`examples/01-read-through-cache`](examples/01-read-through-cache).

## API

```go
func MustNew[K comparable, V any](cfg r8e.CacheConfig) r8e.Cache[K, V]
```

Renvoie un `r8e.Cache[K, V]` adossé à un cache Otter avec TTL par entrée. La
valeur retournée expose les trois méthodes de `r8e.Cache` :

| Méthode | Comportement |
|---|---|
| `Get(key K) (V, bool)` | récupère une valeur en cache |
| `Set(key K, value V, ttl time.Duration)` | stocke une valeur avec un TTL par entrée |
| `Delete(key K)` | supprime une entrée |

## Notes

- Seul `MaxSize` de `r8e.CacheConfig` est utilisé (la capacité du cache). Le TTL
  de fraîcheur est appliqué **à chaque appel `Set`** par r8e, pas lu depuis la
  config.
- `MustNew` **panique** si le cache Otter sous-jacent ne peut être construit
  (p. ex. `MaxSize: 0`) — appelez-la au démarrage, comme les autres
  constructeurs `Must*`.
- Pour la politique read-through, instanciez le type de valeur en
  `r8e.CacheEntry[V]` :
  `otteradapter.MustNew[string, r8e.CacheEntry[string]](cfg)`. Otter lui-même est
  réutilisé tel quel — l'enveloppe `CacheEntry` porte les métadonnées de
  fraîcheur de r8e.

Voir le [README principal de r8e](../README.fr.md#cache) pour la documentation
complète de la politique de cache (read-through, stale-if-error, negative
caching, refresh-ahead).
