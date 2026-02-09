*[Read in English](README.md)*

# Exemple 16 — Convenience Do

Montre la fonction utilitaire `r8e.Do` pour des appels resilients ponctuels
sans creer de policy nommee.

## Ce que cet exemple demontre

`r8e.Do[T]` cree une policy anonyme (sans nom) en interne, execute la
fonction a travers celle-ci et retourne le resultat. La policy n'est
enregistree dans aucun `Registry` et est ecartee apres l'appel.

Trois scenarios :

1. **Retry + timeout** — Une fonction qui echoue deux fois avant de reussir
   est encapsulee avec retry et timeout. Les retries recuperent les erreurs
   transitoires et le resultat est retourne.

2. **Fallback** — Une fonction qui echoue systematiquement est encapsulee
   avec retry et un fallback statique. Une fois les retries epuises, la
   valeur de fallback est retournee.

3. **Sans options (pass-through)** — Appeler `Do` sans options cree une
   policy nue qui transmet simplement l'appel a la fonction. Cela equivaut
   a appeler la fonction directement.

## Quand l'utiliser

- Appels ponctuels pour lesquels creer et nommer une policy n'est pas
  justifie.
- Prototypage rapide ou scripts ou l'on souhaite de la resilience sans
  configuration prealable.
- Tests ou benchmarks d'une fonction avec differentes options de resilience.

Pour les services en production, preferez `NewPolicy` avec un nom afin que la
policy s'enregistre dans le systeme de health/readiness.

## Concepts cles

| Concept | Detail |
|---|---|
| `r8e.Do[T](ctx, fn, opts...)` | Appel resilient ponctuel sans policy nommee |
| Policy anonyme | Non enregistree dans un `Registry` ; pas de reporting de sante |
| Memes options | Accepte les memes options `With*` que `NewPolicy` |

## Execution

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
