*[Read in English](README.md)*

# Exemple 06 — Bulkhead

Illustre la limitation de concurrence a l'aide du patron bulkhead (semaphore).

## Ce que cet exemple illustre

Une politique est configuree avec `WithBulkhead(3)`, limitant les appels
concurrents a 3. L'exemple lance 6 goroutines qui tentent toutes d'appeler
`policy.Do` simultanement. Chaque appel simule 200 ms de travail.

- Les 3 premieres goroutines acquierent un emplacement dans le bulkhead et
  terminent leur travail.
- Les 3 goroutines restantes sont immediatement rejetees avec
  `ErrBulkheadFull` car tous les emplacements sont occupes.

Un leger decalage (10 ms) entre les lancements de goroutines garantit que les
3 premiers appels occupent activement les emplacements lorsque les 3 suivants
arrivent.

## Concepts cles

| Concept | Detail |
|---|---|
| `WithBulkhead(n)` | Limite les executions concurrentes a `n` a l'aide d'un semaphore |
| `ErrBulkheadFull` | Erreur sentinelle renvoyee lorsque tous les emplacements du bulkhead sont occupes |
| Non-bloquant | Le bulkhead ne met pas les requetes en file d'attente -- les appels excedentaires sont rejetes immediatement |
| Isolation | Empeche une dependance lente de consommer toutes les goroutines ou connexions |

## Quand l'utiliser

- Pour proteger des ressources partagees (pools de connexions a la base de
  donnees, API externes) contre un nombre excessif de requetes concurrentes.
- Pour isoler les domaines de defaillance afin qu'un service aval lent ne
  provoque pas un epuisement en cascade des ressources.
- En complement de la limitation de debit : la limitation de debit controle le
  debit dans le temps, le bulkhead controle l'acces concurrent a un instant
  donne.

## Execution

```bash
go run ./examples/06-bulkhead/
```

## Sortie attendue

Trois workers se terminent avec succes ; trois sont rejetes avec
`REJECTED (bulkhead full)`. L'ordre exact depend de l'ordonnancement des
goroutines.
