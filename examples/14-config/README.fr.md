*[Read in English](README.md)*

# Exemple 14 — Configuration JSON

Montre le chargement de la configuration des policies depuis un fichier JSON
et la recuperation de policies typees a l'execution avec `r8econf.GetPolicy`.
Le chargement de fichier vit dans le paquet edge `r8econf`, afin que le paquet
principal `r8e` reste sans dependance.

## Ce que cet exemple demontre

### Chargement de la configuration

`r8econf.Load("config.json")` lit et valide un fichier de configuration JSON
et retourne un `*r8econf.Store`. Toutes les policies sont validees de maniere
anticipee au moment du chargement — les durees invalides, les strategies de
backoff inconnues ou le JSON mal forme produisent des erreurs immediates.

Le fichier `config.json` inclus definit deux policies :

- **payment-api** — timeout, circuit breaker, retry (backoff exponentiel),
  rate limiter et bulkhead
- **notification-api** — timeout et retry (backoff constant)

### Recuperation de policies typees

`r8econf.GetPolicy[string](store, "payment-api", ...)` recupere la
configuration nommee et construit une `Policy[string]` (en retournant une
erreur si la configuration stockee est invalide). Des options supplementaires
au niveau du code (comme `WithFallback`) peuvent completer les parametres
charges depuis la configuration. Les options au niveau du code sont appliquees
apres les options de configuration et ont donc la priorite.

### Noms de policy inconnus

Si `r8econf.GetPolicy` est appele avec un nom qui n'existe pas dans la
configuration, il cree une policy nue avec uniquement les options fournies dans
le code. Cela
permet une migration progressive des policies definies uniquement en code vers
des policies pilotees par la configuration.

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

Strategies de backoff supportees : `"constant"`, `"exponential"`, `"linear"`,
`"exponential_jitter"`.

## Concepts cles

| Concept | Detail |
|---|---|
| `r8econf.Load(path)` | Lit et valide un fichier de configuration JSON, retourne un `*r8econf.Store` |
| `r8econf.GetPolicy[T](store, name, opts...)` | Recupere une policy typee par nom (retourne une erreur) avec des surcharges optionnelles |
| Validation anticipee | Toutes les policies sont validees au moment du chargement |
| Priorite des options | Les options au niveau du code priment sur les options de configuration |

## Execution

```bash
go run ./examples/14-config/
```
