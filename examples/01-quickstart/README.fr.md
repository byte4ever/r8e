*[Read in English](README.md)*

# Exemple 01 — Demarrage rapide

Exemple minimal de bout en bout montrant comment creer une politique de
resilience et executer une fonction a travers celle-ci.

## Ce que cet exemple demontre

- Creation d'une `Policy[string]` avec `NewPolicy`, composant trois patrons en
  un seul appel : **timeout**, **retry** (backoff exponentiel) et **circuit
  breaker**.
- Appel de `policy.Do` pour executer une fonction a travers la chaine de
  middlewares composee.
- r8e trie automatiquement les patrons dans le bon ordre d'execution, quel que
  soit l'ordre dans lequel vous specifiez les options.

## Concepts cles

| Concept | Detail |
|---|---|
| `NewPolicy[T]` | Constructeur de politique generique — `T` est le type de retour de la fonction encapsulee |
| `WithTimeout` | Annule l'appel s'il depasse la duree specifiee |
| `WithRetry` | Retente les echecs transitoires avec la strategie de backoff specifiee |
| `WithCircuitBreaker` | Echoue immediatement lorsque le service en aval est indisponible |
| `policy.Do` | Execute `func(context.Context) (T, error)` a travers la chaine de middlewares |

## Execution

```bash
go run ./examples/01-quickstart/
```

## Sortie attendue

```
result: Hello from r8e!
```
