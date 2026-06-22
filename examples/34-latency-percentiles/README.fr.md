*[Read in English](README.md)*

# Exemple 34 — Percentiles de latence

Illustre les percentiles de latence toujours actifs que chaque politique
enregistre, révélant une queue lente qu'une simple moyenne ferait
silencieusement disparaître.

## Ce que cet exemple illustre

Une politique nue est créée sans aucune option de résilience — `WithTimeout`,
`WithHedge` et les autres sont absentes. Pourtant la politique se mesure
elle-même :

1. La durée de bout en bout de chaque appel `Do()` est injectée dans un sketch
   DDSketch à fenêtre glissante, comme **effet de bord de l'appel** — il n'y a
   aucune option à activer.
2. Un backend est sollicité 200 fois. Il répond en ~10 ms neuf fois sur dix,
   mais le dixième appel prend ~150 ms — une distribution volontairement
   déséquilibrée.
3. `Metrics()` expose les récents **p50/p95/p99**. Le p50 reflète l'appel
   rapide typique ; le p99 garde l'appel lent rare visible.

La leçon tient dans l'écart entre p50 et p99. Une moyenne fondrait l'appel lent
rare dans la majorité rapide et rapporterait un chiffre trompeusement sain, alors
même que les utilisateurs réels tombent sur le chemin lent une fois sur dix.

## Concepts clés

| Concept | Détail |
|---|---|
| `Metrics().LatencyP50` | L'appel typique — le gros de la distribution |
| `Metrics().LatencyP95` / `LatencyP99` | La queue — les appels lents qu'une moyenne masque |
| `Metrics().LatencySamples` | Le nombre d'appels dans la fenêtre glissante courante |
| Instrumentation toujours active | Aucune option à activer ; l'enregistrement est gratuit, à l'image des timers par appel de resilience4j |

## Quand l'utiliser

- Dès que vous voulez une image fidèle du temps de service — les alertes et les
  SLO devraient s'appuyer sur le p99, pas sur une moyenne qui masque la queue.
- Pour alimenter des tableaux de bord ou un timeout/hedge adaptatif qui se règle
  à partir du p99 observé (voir les exemples 35 et 36).
- Pour des politiques à faible trafic ou peu cérémonieuses où vous voulez de
  l'observabilité sans câbler un pipeline de métriques séparé.

## Exécution

```bash
go run ./examples/34-latency-percentiles/
```

## Sortie attendue

Le nombre d'échantillons dans la fenêtre, suivi des p50/p95/p99. Le p50 se situe
près de 10 ms tandis que le p99 se situe bien au-dessus (près du chemin lent à
150 ms). Les valeurs exactes varient car les appels lents tombent
aléatoirement.
