*[Read in English](README.md)*

# Exemple 44 — Classifier du circuit breaker

Montre `CircuitBreakerClassifier`, qui indique au circuit breaker quels appels
renseignent sur la santé du downstream. L'appelant, lui, reçoit toujours la
vraie erreur.

## Ce que cet exemple illustre

Par défaut, le breaker compte toute erreur non nil comme un échec et toute
erreur nil comme un succès. Trois sortes d'appels échappent à cette règle :

- une réponse métier comme « commande introuvable » est une erreur pour
  l'appelant, mais elle prouve que le downstream répond ;
- un appel abandonné par son appelant (deadline dépassée, arrêt du service) ne
  prouve rien, ni dans un sens ni dans l'autre ;
- un appel refusé par le bulkhead ou le rate limiter de la policy n'a jamais
  atteint le downstream. Ces gardes tournent à l'intérieur du breaker, qui voit
  leurs refus.

Compter le premier ou le troisième ouvre le breaker sur un service en bonne
santé. Compter le second comme un échec rouvre un breaker en cours de
rétablissement. Le compter
comme un succès remet le compteur d'échecs à zéro, et un downstream bloqué
n'ouvre alors jamais le breaker.

Le classifier range chaque appel dans `OutcomeSuccess`, `OutcomeFailure` ou
`OutcomeNeutral`. Un appel neutre ne touche ni au compteur d'échecs ni à la
fenêtre des appels lents. En half-open, il libère la sonde sans rien compter.
L'exemple déroule cinq phases. Les breakers des phases 1 à 4 s'ouvrent après
deux échecs :

1. **Réponses métier** : trois erreurs « commande introuvable ». Le classifier
   les rend neutres, et le breaker reste fermé.
2. **Appelants qui abandonnent** : deux appelants avec une deadline de 50 ms
   appellent un downstream qui met 200 ms. `CallerCancellationIsNeutral` rend
   les deux appels neutres, et le breaker reste fermé.
3. **Rafale sur le bulkhead** : sur une seconde policy dont le bulkhead n'a
   qu'une place, un appel occupe cette place pendant que deux autres arrivent.
   Le bulkhead les refuse avec `ErrBulkheadFull`. `IsLocalRejection` rend ces
   refus neutres, et le breaker reste fermé.
4. **Downstream bloqué** : le timeout de la policy (300 ms) coupe deux appels.
   Une deadline posée par r8e n'est jamais une annulation de l'appelant : les
   deux appels comptent comme des échecs, le breaker s'ouvre et le troisième
   appel échoue tout de suite avec `ErrCircuitOpen`.
5. **Appel qui survit à une ouverture** : sur une troisième policy, qu'un
   seul échec ouvre, un appel lent démarre pendant que le breaker est fermé.
   Un appel en échec ouvre le breaker, puis l'appel lent réussit. Son
   résultat est périmé : le breaker l'ignore et reste ouvert.

Les phases 3, 4 et 5 se terminent par les compteurs `CircuitNeutralOutcomes`
et `CircuitStaleOutcomes` de la policy. La policy du bulkhead compte deux
résultats neutres, la policy principale cinq (phases 1 et 2), et la troisième
policy un résultat périmé.

## Fonctionnement

```mermaid
flowchart LR
    Call[policy.Do] --> Admit{le breaker admet ?}
    Admit -- non --> Open[ErrCircuitOpen]
    Admit -- oui --> Fn[appel au downstream]
    Fn --> Classify[classifier ctx, err]
    Classify -- OutcomeSuccess --> Reset[compteur d'échecs remis à zéro]
    Classify -- OutcomeFailure --> Count[échec compté]
    Classify -- OutcomeNeutral --> Nothing[breaker inchangé]
    Fn --> Caller[l'appelant reçoit la vraie erreur]
```

## Concepts clés

| Concept | Détail |
|---|---|
| `CircuitBreakerClassifier(fn)` | Définit comment le middleware de la policy transforme `(ctx, err)` en `Outcome`. Nil rétablit le défaut |
| `OutcomeSuccess` / `OutcomeFailure` | Ce que renvoie le classifier par défaut pour une erreur nil / non nil |
| `OutcomeNeutral` | L'appel ne dit rien du downstream : rien n'est compté, et une sonde half-open est libérée |
| `IsCallerCancellation(ctx, err)` | Vrai quand le contexte de l'appelant a mis fin à l'appel, cause comprise ; faux pour une deadline posée par r8e (`WithTimeout`, `PerAttemptTimeout` d'un retry, budget `PropagateDeadline` qui tombe avant la deadline de l'appelant) |
| `CallerCancellationIsNeutral` | Classifier prêt à l'emploi : l'annulation par l'appelant est neutre, le reste est classé comme par défaut |
| `IsLocalRejection(err)` | Vrai quand `err` est lui-même, sans enveloppe, l'une des erreurs `ErrRateLimited`, `ErrBulkheadFull`, `ErrBulkheadTimeout`, `ErrCoDelShed`, `ErrConcurrencyLimited` : ce que renvoie une garde placée à l'intérieur du breaker. Une sentinelle que la fonction renvoie elle-même telle quelle est reconnue aussi : enveloppez-la à cet endroit |
| `CircuitNeutralOutcomes` / `CircuitStaleOutcomes` | Compteurs de `PolicyMetrics`, avec les hooks `OnCircuitNeutral` / `OnCircuitStaleOutcome` : les résultats que le breaker a mis de côté comme neutres, et ceux qu'il a ignorés comme périmés. Un résultat périmé compte une seule fois, comme périmé, même s'il est neutre |
| `CircuitBreaker.Acquire` / `CircuitPermit.Done` | L'API autonome derrière le middleware ; un résultat antérieur à la dernière ouverture du breaker est ignoré |

## Quand l'utiliser

- Avec un downstream qui renvoie des erreurs métier (introuvable, requête
  invalide, conflit) qui ne doivent pas ouvrir le breaker.
- Avec des appelants dont la deadline est plus courte que le timeout de la
  policy, ou des services qui annulent les appels en cours à l'arrêt.
- Avec un bulkhead, un rate limiter ou une adaptive concurrency à côté du
  breaker : une rafale qu'ils refusent ne doit pas l'ouvrir.
- Pour détecter un downstream bloqué alors que les appelants abandonnent avant,
  gardez un `WithTimeout` plus court que leurs deadlines : le timeout de la
  policy compte toujours comme un échec.

## Exécution

```bash
go run ./examples/44-circuit-breaker-classifier/
```

## Sortie attendue

```text
=== Phase 1: "not found" answers do not count ===
  call 1: order not found (state=closed)
  call 2: order not found (state=closed)
  call 3: order not found (state=closed)

=== Phase 2: callers that give up do not count ===
  call 1: context deadline exceeded (state=closed)
  call 2: context deadline exceeded (state=closed)

=== Phase 3: a burst past the bulkhead does not count ===
  call 1: bulkhead full (state=closed)
  call 2: bulkhead full (state=closed)
  the call holding the slot: order 7 (state=closed)
  classifier-demo-bulkhead: neutral outcomes=2, stale outcomes=0

=== Phase 4: a hanging downstream still opens the breaker ===
  call 1: timeout (state=closed)
  [hook] circuit OPENED
  call 2: timeout (state=open)
  call 3: circuit breaker is open (state=open)
  classifier-demo: neutral outcomes=5, stale outcomes=0

=== Phase 5: a call that outlives a trip does not count ===
  call 1: downstream down (state=open)
  the slow call: order 9 (state=open)
  classifier-demo-stale: neutral outcomes=0, stale outcomes=1
```
