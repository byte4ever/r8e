*[Read in English](README.md)*

# Exemple 35 — Timeout adaptatif

Illustre un timeout adaptatif piloté par percentile qui dimensionne l'échéance
de chaque appel à partir de la latence récente du backend lui-même, plutôt que
d'une constante devinée.

## Ce que cet exemple illustre

Une politique est configurée avec `WithTimeout(1s, AdaptiveTimeout(...))`. Une
fois suffisamment d'appels réussis observés :

1. L'échéance est calculée à partir d'une fenêtre glissante de latences
   **réussies** récentes selon `clamp(p99 × multiplicateur, plancher, plafond)`.
2. Le `1s` passé à `WithTimeout` devient le **plafond** dur et le repli de
   préchauffage — la valeur adaptative ne peut que se resserrer en dessous,
   jamais le dépasser.
3. Avec un backend stable à ~10 ms, le timeout adaptatif se stabilise autour de
   20 ms (`p99 × 2`), bien en dessous du plafond de 1 s, si bien qu'un véritable
   traînard est coupé rapidement.

Seuls les appels **réussis** alimentent la fenêtre : un déclenchement de timeout
ne peut donc jamais gonfler le percentile même qui l'a fixé. Quand la latence du
backend dérive, l'échéance la suit — pas de redéploiement pour réajuster une
constante.

## Concepts clés

| Concept | Détail |
|---|---|
| `WithTimeout(plafond, AdaptiveTimeout(...))` | La durée est le plafond dur et le repli de préchauffage, pas la valeur de fonctionnement |
| `AdaptiveTimeoutPercentile(0.99)` | Dimensionne l'échéance sur le p99 — le ~1 % d'appels sains les plus lents |
| `AdaptiveTimeoutMultiplier(2.0)` | Marge au-dessus du percentile pour que la gigue normale ne déclenche jamais le timeout |
| `AdaptiveTimeoutFloor(20ms)` | Borne inférieure pour qu'une fenêtre ultra-rapide ne réduise pas le timeout à presque zéro |
| `Metrics().AdaptiveTimeout` | L'échéance que la politique appliquerait actuellement |

## Quand l'utiliser

- Backends dont la latence varie ou dérive dans le temps, où tout timeout fixe
  unique est soit trop serré (coupant des appels sains) soit trop lâche
  (laissant les traînards monopoliser des emplacements).
- Services où vous suivez déjà un p99 stable et voulez que le timeout le suive
  automatiquement plutôt que par réglage manuel périodique.
- À associer à un disjoncteur ou un budget de réessais pour que l'échéance
  resserrée transforme les traînards en échecs rapides et contenus.

## Exécution

```bash
go run ./examples/35-adaptive-timeout/
```

## Sortie attendue

Après le préchauffage : le p99 observé (~10 ms), le timeout adaptatif (~20 ms,
noté « was a 1s ceiling »), et le nombre de timeouts (généralement 0). Les
valeurs exactes en millisecondes varient légèrement d'une exécution à l'autre.
