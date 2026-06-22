*[Read in English](README.md)*

# Exemple 22 — Budget de temps

Illustre `WithTimeBudget`, un budget de temps **total** partagé par toute la
chaîne de retries, qui arrête de réessayer dès que continuer ne pourrait plus
aboutir à temps.

## Ce que cet exemple illustre

Un timeout par tentative borne chaque tentative isolément : cinq retries peuvent
donc cumuler jusqu'à cinq fois le timeout. Un budget de temps borne au contraire
la **somme** de tout ce travail. L'exemple exécute deux fois la même opération
toujours en échec :

1. **Sans budget** — `WithRetry(5, ExponentialBackoff(100ms))` déroule toute la
   chaîne en dormant 100, 200, 400, 800 ms entre les tentatives (~1,5 s au total)
   avant d'abandonner.
2. **Avec un budget de 350 ms** — après les backoffs de 100 ms et 200 ms (~300 ms
   consommés), le prochain sommeil de 400 ms dépasserait le budget restant. Le
   retry s'arrête tôt et renvoie `ErrTimeBudgetExceeded` (qui enveloppe l'erreur
   réelle du downstream) au lieu d'attendre une tentative vouée à l'échec.

Observez le nombre de tentatives et le temps écoulé affichés pour chacune : la
version budgétée fait moins de tentatives et revient bien plus tôt.

## Concepts clés

| Concept | Détail |
|---|---|
| `WithTimeBudget(d)` | Définit un budget total pour l'appel, partagé par retry et hedge |
| Arrêt anticipé | Avant chaque retry, si le backoff seul dépasserait le budget restant, le retry s'arrête avec `ErrTimeBudgetExceeded` au lieu de dormir |
| Plus strict qu'un timeout par tentative | Un timeout par tentative borne chaque tentative ; le budget borne leur somme |
| Coopératif | Le budget contrôle si du travail supplémentaire *démarre* ; il n'annule pas une tentative en cours (associez `WithTimeout` pour une échéance ferme) |
| Nécessite un consommateur | Seuls retry et hedge consomment le budget : il requiert `WithRetry` ou `WithHedge` — sans aucun des deux, `NewPolicy` panique |

## Quand l'utiliser

- Appels avec une véritable échéance amont (un SLA de requête entrante) où le
  temps total à travers les retries doit être borné, pas seulement chaque
  tentative.
- En complément d'un backoff exponentiel, où les derniers sommeils dominent et où
  l'on préfère échouer vite plutôt qu'en attendre un.
- Associez `WithTimeout` lorsqu'il faut aussi borner une tentative bloquée — le
  budget seul n'annule pas le travail en cours.

## Exécution

```bash
go run ./examples/22-time-budget/
```

## Sortie attendue

Deux exécutions de la même opération en échec. La version sans budget réalise
toutes les tentatives sur ~1,5 s ; la version budgétée à 350 ms fait moins de
tentatives et revient autour de 300 ms avec `ErrTimeBudgetExceeded`. Les durées
exactes varient légèrement selon l'ordonnancement.
