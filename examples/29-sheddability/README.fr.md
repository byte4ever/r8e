*[Read in English](README.md)*

# Exemple 29 — Délestabilité des requêtes

Illustre la priorité de délestage par appel avec `WithSheddability` : lorsqu'un
backend est surchargé, le travail d'arrière-plan cède en premier tandis que le
trafic destiné aux utilisateurs continue de passer.

## Ce que cet exemple illustre

Un limiteur qui déleste aveuglément abandonne les requêtes critiques et
différables avec la même probabilité — un mauvais résultat, puisque c'est le
travail d'arrière-plan, peu coûteux, qui devrait céder en premier.
`WithSheddability` estampille la priorité de chaque appel sur son contexte, et le
limiteur adaptatif la respecte :

1. **`SheddabilityAlways`** (arrière-plan) — délesté en premier dès qu'un
   délestage est actif.
2. **`SheddabilityDefault`** (la valeur zéro, sans estampille) — délesté à la
   probabilité SRE normale.
3. **`SheddabilityNever`** (critique, destiné aux utilisateurs) — toujours admis,
   même à charge maximale.

L'exemple lance des rafales des trois classes en tourniquet contre un backend
**sain**, puis **surchargé**, puis **rétabli**. Les taux de passage rendent
l'ordre de priorité visible : sous charge, l'arrière-plan tombe à près de zéro,
le défaut chute partiellement et le critique reste à 100 %.

## Fonctionnement

Le limiteur adaptatif surveille le ratio succès/échec sur une fenêtre glissante
et commence à rejeter les appels **localement** — avant même qu'ils ne quittent
le processus — dès que les acceptations dépassent la capacité du backend (le
modèle de limitation côté client de Google SRE). L'estampille de délestabilité
décide *quels* appels il rejette en premier. Un appel délesté localement
n'exécute jamais `fn` et revient sous forme d'`ErrThrottled` ; l'absence de cette
erreur signifie donc que l'appel a atteint le backend.

## Concepts clés

| Concept | Détail |
|---|---|
| `WithSheddability(ctx, niveau)` | Estampille la priorité de délestage d'un appel sur son contexte |
| `SheddabilityNever` | Contournement — trafic critique, toujours admis |
| `SheddabilityDefault` | Valeur zéro — délesté à la probabilité SRE normale |
| `SheddabilityAlways` | Délesté en premier — travail d'arrière-plan ou spéculatif |
| `WithAdaptiveThrottle(...)` | Limiteur côté client qui lit l'estampille ; `OverloadRatio`, `MinRequests`, `ThrottleWindow`, `MaxRejectionRate` règlent sa sensibilité |
| `ErrThrottled` | Renvoyé par un appel délesté localement ; `fn` n'a jamais été invoqué |

## Quand l'utiliser

- Les charges mixtes où des requêtes destinées aux utilisateurs partagent une
  politique avec des tâches d'arrière-plan (réindexation, préchargement,
  analytique) et où vous voulez que le travail d'arrière-plan absorbe la
  surcharge.
- Les appels spéculatifs ou au mieux qu'il est sûr d'abandonner sous pression —
  marquez-les `SheddabilityAlways`.
- Les chemins critiques (paiement, authentification) qui doivent atteindre le
  backend même pendant un délestage partiel — marquez-les `SheddabilityNever`.

## Exécution

```bash
go run ./examples/29-sheddability/
```

## Sortie attendue

Trois rafales étiquetées. Dans la rafale **saine**, les trois classes atteignent
le backend. Dans la rafale **surchargée**, l'arrière-plan chute fortement
(souvent à 0), le défaut chute partiellement et le critique conserve sa part
complète ; le compteur de délestage local est élevé. Dans la rafale **rétablie**,
le délestage cesse et toutes les classes repassent. Les comptes de passage exacts
varient selon le minutage, car le limiteur est probabiliste.
