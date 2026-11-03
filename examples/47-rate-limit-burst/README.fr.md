*[Read in English](README.md)*

# Exemple 47 — Burst du rate limiter

Montre `Burst`, qui fixe la taille du token bucket du rate limiter
indépendamment du débit. Vous choisissez ainsi combien d'appels peuvent partir
en même temps.

## Ce qu'il démontre

Par défaut, le bucket contient une seconde de tokens, avec un minimum d'un
token, et il démarre plein. À 10 req/s, 10 appels peuvent partir au même
instant : au démarrage, ou après une seconde sans trafic. `Burst(n)` fixe la
taille du bucket à `n` tokens. Jusqu'à `n` appels partent d'un coup, puis les
appels suivent le débit.

L'exemple pilote ses policies avec une horloge manuelle (`WithClock`) : la
sortie ne dépend pas de la vitesse de la machine. Il déroule trois phases :

1. **Bucket par défaut** : avec `WithRateLimit(10)`, 10 des 12 appels lancés
   au même instant passent.
2. **`Burst(3)`** : avec `WithRateLimit(10, r8e.Burst(3))`, 3 des 12 appels
   passent. Ensuite, un token revient toutes les 100 ms, et chaque pas de
   100 ms laisse passer un appel sur deux.
3. **Reload** : un reload du débit (`rate_limit` à 20) garde le burst. Après
   une pause d'une seconde, seuls 3 appels partent d'un coup. Un reload de
   `rate_limit_burst` à 6 change le burst, et après la pause suivante 6 appels
   partent. Les pauses laissent le bucket se remplir : un reload n'ajoute pas
   de tokens. Un reload ne peut pas ajouter un burst à une policy créée sans
   burst : `Reconfigure` sur la policy de la phase 1 renvoie
   `ErrPatternAbsent`.

## Concepts clés

| Concept | Détail |
|---|---|
| `Burst(n)` | Capacité du bucket de `n` tokens, indépendante du débit. Le bucket démarre plein : `n` appels peuvent partir d'un coup au démarrage |
| Capacité par défaut | Une seconde de tokens, et au moins un token : un débit sous 1 req/s laisse donc passer des appels |
| Plage | `n` entre 1 et `r8e.MaxBurst` ; toute autre valeur fait paniquer `NewPolicy` avec `ErrRateLimitBurstOutOfRange` |
| `Reconfigure` et AIMD | Ils changent le débit, jamais une capacité fixée par `Burst` |
| Débit nul ou négatif | Vide le bucket, avec ou sans `Burst`. Un débit positif ensuite rétablit la capacité de `Burst`, avec un bucket vide |
| `rate_limit_burst` | Champ JSON, exige `rate_limit`. Un reload peut changer le burst d'une policy créée avec un burst, pas en ajouter un (`ErrPatternAbsent`). Un reload sans ce champ garde le burst actuel |
| Quota sur une fenêtre | Un bucket ne tient un quota que sur sa fenêtre de remplissage : `WithRateLimit(0.125, r8e.Burst(450))` peut laisser passer jusqu'à 900 appels en une heure, alors que `Burst(1)` ne dépasse jamais 451 |

## Quand l'utiliser

- Avec un downstream qui n'accepte que quelques appels à la fois, même si son
  débit est plus élevé.
- Avec un quota compté sur une fenêtre glissante : gardez un burst petit,
  comme le montre le tableau.
- Pour lisser un démarrage : `Burst(1)` espace les appels dès le premier.

## Lancer

```bash
go run ./examples/47-rate-limit-burst/
```

## Sortie attendue

`+` marque un appel admis, `x` un appel refusé avec `ErrRateLimited`.

```text
=== Phase 1: default bucket, one second of tokens ===
  12 calls at t=0: ++++++++++xx (10 admitted)

=== Phase 2: Burst(3), then the steady rate ===
  12 calls at t=0: +++xxxxxxxxx (3 admitted)
  +100ms, 2 calls: +x (1 admitted)
  +100ms, 2 calls: +x (1 admitted)
  +100ms, 2 calls: +x (1 admitted)

=== Phase 3: reload ===
  reload rate_limit=20: ok
  after 1s, 12 calls: +++xxxxxxxxx (3 admitted)
  reload rate_limit_burst=6: ok
  after 1s, 12 calls: ++++++xxxxxx (6 admitted)
  reload rate_limit_burst=6 on rl-default: r8e: cannot reconfigure a pattern the policy does not have: "rate_limit_burst"
```
