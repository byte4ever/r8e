*[Read in English](README.md)*

# Exemple 14 — Configuration JSON

Montre le chargement de la configuration des policies depuis un fichier JSON
et la récupération de policies typées à l'exécution avec `r8econf.GetPolicy`.
Le chargement de fichier vit dans le paquet edge `r8econf`, afin que le paquet
principal `r8e` reste sans dépendance.

## Ce que cet exemple démontre

### Chargement de la configuration

`r8econf.Load("config.json")` lit et valide un fichier de configuration JSON
et retourne un `*r8econf.Store`. Toutes les policies sont validées de manière
anticipée au moment du chargement — les durées invalides, les stratégies de
backoff inconnues ou le JSON mal formé produisent des erreurs immédiates.

Le fichier `config.json` inclus définit deux policies :

- **payment-api** — timeout, circuit breaker, retry (backoff exponentiel),
  rate limiter et bulkhead
- **notification-api** — timeout et retry (backoff constant)

### Récupération de policies typées

`r8econf.GetPolicy[string](store, "payment-api", ...)` récupère la
configuration nommée et construit une `Policy[string]` (en retournant une
erreur si la configuration stockée est invalide). Des options supplémentaires
au niveau du code (comme `WithFallback`) peuvent compléter les paramètres
chargés depuis la configuration. Les options au niveau du code sont appliquées
après les options de configuration et ont donc la priorité.

### Noms de policy inconnus

Si `r8econf.GetPolicy` est appelé avec un nom qui n'existe pas dans la
configuration, il crée une policy nue avec uniquement les options fournies dans
le code. Cela
permet une migration progressive des policies définies uniquement en code vers
des policies pilotées par la configuration.

## Format de configuration

```json
{
  "policies": {
    "policy-name": {
      "timeout": "2s",
      "circuit_breaker": {
        "failure_threshold": 5,
        "recovery_timeout": "30s"
      },
      "retry": {
        "max_attempts": 3,
        "backoff": "exponential",
        "base_delay": "100ms",
        "max_delay": "5s"
      },
      "rate_limit": 100,
      "bulkhead": 10
    }
  }
}
```

Stratégies de backoff supportées : `"constant"`, `"exponential"`, `"linear"`,
`"exponential_jitter"`.

## Concepts clés

| Concept | Détail |
|---|---|
| `r8econf.Load(path)` | Lit et valide un fichier de configuration JSON, retourne un `*r8econf.Store` |
| `r8econf.GetPolicy[T](store, name, opts...)` | Récupère une policy typée par nom (retourne une erreur) avec des surcharges optionnelles |
| Validation anticipée | Toutes les policies sont validées au moment du chargement |
| Priorité des options | Les options au niveau du code priment sur les options de configuration |

## Exécution

```bash
go run ./examples/14-config/
```
