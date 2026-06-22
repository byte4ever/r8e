*[Read in English](README.md)*

# Exemple 31 — Recover (panic → erreur)

Illustre `WithRecover`, qui intercepte un panic émis par la fonction utilisateur
et le transforme en `*r8e.PanicError` au lieu de laisser le processus planter —
de sorte que le reste de la chaîne de résilience puisse le réessayer, basculer
vers un repli ou le journaliser comme n'importe quelle autre erreur.

## Ce que cet exemple illustre

Un seul panic non récupéré au fond d'un gestionnaire fait normalement tomber
l'instance entière, emportant avec lui toutes les requêtes en cours.
`WithRecover` enveloppe l'appel le plus interne et convertit le panic en une
valeur d'erreur ordinaire. L'exemple parcourt trois façons d'en tirer parti :

1. **Panic → erreur.** Une politique avec un simple `WithRecover` intercepte le
   panic ; le hook `OnPanic` se déclenche, et l'erreur renvoyée se déballe en un
   `*PanicError` portant la valeur d'origine du panic et la trace de pile
   capturée au moment de la récupération.
2. **Panic + repli.** Comme le panic est désormais une erreur, `WithFallback`
   substitue une valeur par défaut sûre — l'appelant obtient une valeur
   exploitable et une erreur `nil`, le panic étant absorbé de bout en bout.
3. **Panic puis réessai.** Un panic transitoire qui disparaît au prochain essai
   n'est qu'un échec réessayable de plus. `WithRetry` relance l'appel et il
   réussit ; `PanicsRecovered` comptabilise la récupération.

## Concepts clés

| Concept | Détail |
|---|---|
| `WithRecover()` | Enveloppe l'appel le plus interne ; convertit tout panic en `*PanicError` au lieu de planter |
| `ErrPanic` / `*PanicError` | Sentinelle pour `errors.Is` ; l'erreur concrète (via `errors.As`) porte `.Value` et `.Stack` |
| Hook `OnPanic` | Se déclenche au moment de la récupération — l'endroit pour brancher métriques ou alertes |
| `WithFallback` | Traite le panic récupéré comme tout appel en échec et renvoie une valeur par défaut |
| `WithRetry` | Relance l'appel quand le panic est transitoire ; recover est le plus interne, donc chaque essai est enveloppé |
| `Metrics().PanicsRecovered` | Compteur incrémenté à chaque panic intercepté |

## Quand l'utiliser

- Lors d'appels vers du code auquel vous ne faites pas pleinement confiance —
  bibliothèques tierces, plugins ou gestionnaires où un panic sur une entrée
  invalide ferait autrement planter le serveur.
- Partout où l'échec d'une seule requête ne doit pas faire tomber toutes les
  autres requêtes partageant le processus.
- Lorsque vous voulez que les panics traversent la même mécanique de réessai /
  repli / disjoncteur que les erreurs normales, plutôt que de les traiter à part.

## Exécution

```bash
go run ./examples/31-recover/
```

## Sortie attendue

Trois scénarios étiquetés. Le premier affiche la valeur du panic intercepté et la
première ligne de sa trace de pile ; le deuxième montre la valeur de repli avec
une erreur `nil` ; le troisième montre le premier essai en panic, le réessai
réussi et un compteur `panics_recovered=1`. La sortie est déterministe (aucun
aléa), hormis la ligne exacte de la trace de pile, qui dépend du moteur
d'exécution Go.
