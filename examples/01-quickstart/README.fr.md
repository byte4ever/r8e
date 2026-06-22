*[Read in English](README.md)*

# Exemple 01 — Démarrage rapide

Exemple minimal de bout en bout montrant comment créer une politique de
résilience et exécuter une fonction à travers celle-ci.

## Ce que cet exemple démontre

- Création d'une `Policy[string]` avec `NewPolicy`, qui compose trois patrons en
  un seul appel : **timeout**, **retry** (backoff exponentiel) et **circuit
  breaker**.
- Appel de `policy.Do` pour exécuter une fonction à travers la chaîne de
  middlewares composée.
- r8e trie automatiquement les patrons dans un ordre d'exécution cohérent,
  quel que soit l'ordre dans lequel vous spécifiez les options.

## Concepts clés

| Concept | Détail |
|---|---|
| `NewPolicy[T]` | Constructeur de politique générique — `T` est le type de retour de la fonction encapsulée |
| `WithTimeout` | Annule l'appel s'il dépasse la durée spécifiée |
| `WithRetry` | Retente les échecs transitoires avec la stratégie de backoff spécifiée |
| `WithCircuitBreaker` | Échoue immédiatement lorsque le service en aval est indisponible |
| `policy.Do` | Exécute `func(context.Context) (T, error)` à travers la chaîne de middlewares |

## Exécution

```bash
go run ./examples/01-quickstart/
```

## Sortie attendue

```
result: Hello from r8e!
```
