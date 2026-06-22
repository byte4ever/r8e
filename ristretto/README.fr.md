# r8e/ristretto

[English](README.md) · [Français]

Adaptateur de cache Ristretto pour [**r8e**](../README.fr.md). Il implémente
l'interface `r8e.Cache` au-dessus de
[Ristretto](https://github.com/dgraph-io/ristretto) — le cache de Dgraph basé sur
l'admission et conscient du coût — afin d'alimenter la politique de cache
read-through de r8e (`r8e.WithCache` / `r8e.ReadThroughCache`) ou le
`r8e.StaleCache` autonome avec un cache de qualité production.

## Installation

```bash
go get github.com/byte4ever/r8e/ristretto
```

## Démarrage rapide

```go
import (
    "github.com/byte4ever/r8e"
    ristrettoadapter "github.com/byte4ever/r8e/ristretto"
)

// Le cache stocke des r8e.CacheEntry[V] (l'enveloppe de fraîcheur), avec une clé
// string — le même type de clé que r8e.WithCache. La clé doit satisfaire Key.
cache := ristrettoadapter.MustNew[string, r8e.CacheEntry[string]](
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

Voir [`examples/01-read-through-cache`](examples/01-read-through-cache).

## API

```go
func MustNew[K Key, V any](cfg r8e.CacheConfig) r8e.Cache[K, V]

type Key interface {
    uint64 | string | byte | int | int32 | uint32 | int64
}
```

Renvoie un `r8e.Cache[K, V]` adossé à un cache Ristretto. `K` doit satisfaire
`Key` (le sous-ensemble comparable des types de clé Ristretto). La valeur
retournée expose les trois méthodes de `r8e.Cache` :

| Méthode | Comportement |
|---|---|
| `Get(key K) (V, bool)` | récupère une valeur en cache |
| `Set(key K, value V, ttl time.Duration)` | stocke une valeur (coût 1) avec un TTL par entrée |
| `Delete(key K)` | supprime une entrée |

## Notes

- Seul `MaxSize` de `r8e.CacheConfig` est utilisé. Il borne le **nombre
  d'entrées** : chaque entrée est stockée avec un coût fixe de 1, et
  `NumCounters` est dimensionné à `10 × MaxSize` selon la recommandation de
  Ristretto. Le TTL de fraîcheur est appliqué **à chaque appel `Set`** par r8e,
  pas lu depuis la config.
- **Admission asynchrone.** Ristretto admet les écritures via un buffer : une
  valeur `Set` lors d'un appel peut ne pas être visible au `Get` immédiatement
  suivant, et des écritures peuvent être abandonnées sous pression du buffer.
  C'est sans danger avec r8e : une écriture abandonnée ou pas encore admise se
  dégrade simplement en cache miss (la couche read-through réexécute), jamais en
  valeur erronée. Otter offre une sémantique lecture-après-écriture immédiate
  plus forte si nécessaire — voir [`r8e/otter`](../otter/README.fr.md).
- `MustNew` **panique** si le cache Ristretto sous-jacent ne peut être construit
  — appelez-la au démarrage.

Voir le [README principal de r8e](../README.fr.md#cache) pour la documentation
complète de la politique de cache (read-through, stale-if-error, negative
caching, refresh-ahead).
