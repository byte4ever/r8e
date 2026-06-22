*[Read in English](README.md)*

# Exemple 06 — Bulkhead

Illustre la limitation de concurrence à l'aide du patron bulkhead (sémaphore).

## Ce que cet exemple illustre

Une politique est configurée avec `WithBulkhead(3)`, ce qui limite les appels
concurrents à 3. L'exemple lance 6 goroutines qui tentent toutes d'appeler
`policy.Do` simultanément. Chaque appel simule 200 ms de travail.

- Les 3 premières goroutines acquièrent un emplacement dans le bulkhead et
  terminent leur travail.
- Les 3 goroutines restantes sont immédiatement rejetées avec
  `ErrBulkheadFull`, car tous les emplacements sont occupés.

Un léger décalage (10 ms) entre les lancements de goroutines garantit que les
3 premiers appels occupent activement les emplacements lorsque les 3 suivants
arrivent.

## Concepts clés

| Concept | Détail |
|---|---|
| `WithBulkhead(n)` | Limite les exécutions concurrentes à `n` à l'aide d'un sémaphore |
| `ErrBulkheadFull` | Erreur sentinelle renvoyée lorsque tous les emplacements du bulkhead sont occupés |
| Non-bloquant | Le bulkhead ne met pas les requêtes en file d'attente — les appels excédentaires sont rejetés immédiatement |
| Isolation | Empêche une dépendance lente de consommer toutes les goroutines ou connexions |

## Quand l'utiliser

- Pour protéger des ressources partagées (pools de connexions à la base de
  données, API externes) contre un nombre excessif de requêtes concurrentes.
- Pour isoler les domaines de défaillance afin qu'un service aval lent ne
  provoque pas un épuisement en cascade des ressources.
- En complément de la limitation de débit : la limitation de débit contrôle le
  débit dans le temps, le bulkhead contrôle l'accès concurrent à un instant
  donné.

## Exécution

```bash
go run ./examples/06-bulkhead/
```

## Sortie attendue

Trois workers se terminent avec succès ; trois sont rejetés avec
`REJECTED (bulkhead full)`. L'ordre exact dépend de l'ordonnancement des
goroutines.
