*[Read in English](README.md)*

# Exemple 16 — Convenience Do

Montre la fonction utilitaire `r8e.Do` pour des appels résilients ponctuels
sans créer de policy nommée.

## Ce que cet exemple démontre

`r8e.Do[T]` crée une policy anonyme (sans nom) en interne, exécute la
fonction à travers celle-ci et retourne le résultat. La policy n'est
enregistrée dans aucun `Registry` et est écartée après l'appel.

Trois scénarios :

1. **Retry + timeout** — Une fonction qui échoue deux fois avant de réussir
   est encapsulée avec retry et timeout. Les retries récupèrent les erreurs
   transitoires et le résultat est retourné.

2. **Fallback** — Une fonction qui échoue systématiquement est encapsulée
   avec retry et un fallback statique. Une fois les retries épuisés, la
   valeur de fallback est retournée.

3. **Sans options (pass-through)** — Appeler `Do` sans options crée une
   policy nue qui transmet l'appel à la fonction. Cela équivaut à appeler
   la fonction directement.

## Quand l'utiliser

- Appels ponctuels pour lesquels créer et nommer une policy n'est pas
  justifié.
- Prototypage rapide ou scripts où l'on souhaite de la résilience sans
  configuration préalable.
- Tests ou benchmarks d'une fonction avec différentes options de résilience.

Pour les services en production, préférez `NewPolicy` avec un nom afin que la
policy s'enregistre dans le système de health/readiness.

## Concepts clés

| Concept | Détail |
|---|---|
| `r8e.Do[T](ctx, fn, opts...)` | Appel résilient ponctuel sans policy nommée |
| Policy anonyme | Non enregistrée dans un `Registry` ; pas de reporting de santé |
| Mêmes options | Accepte les mêmes options `With*` que `NewPolicy` |

## Exécution

```bash
go run ./examples/16-convenience-do/
```

## Sortie attendue

```
=== One-off call with retry + timeout ===
  attempt 1
  attempt 2
  attempt 3
  result: "one-off success", err: <nil>

=== One-off call with fallback ===
  result: "emergency default", err: <nil>

=== One-off call with no options (pass-through) ===
  result: "bare call", err: <nil>
```
