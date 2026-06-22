*[Read in English](README.md)*

# Exemple 19 — Budget de retentatives

Illustre le budget de retentatives adaptatif qui limite les retentatives pendant
une panne en aval, afin qu'une dépendance en difficulté ne soit jamais ensevelie
sous une *tempête de retentatives* déclenchée par ses propres appelants.

## Ce que cet exemple illustre

Une politique est configurée avec `WithRetry(5, …)` plafonnée par
`WithRetryBudget(MaxTokens(4), TokenRatio(0.1))`. Le budget est un seau à jetons :
il démarre plein, chaque échec réessayable retire un jeton, et chaque succès en
restitue `0,1`. Tant que le seau reste à la moitié de sa capacité ou en dessous,
les retentatives sont supprimées — la première tentative de chaque appel
s'exécute toujours, mais elle n'est plus amplifiée en charge supplémentaire.

L'exemple se déroule en trois actes :

1. **La panne commence.** Le seau est plein, donc le premier appel dépense son
   budget en vraies retentatives — il effectue plusieurs tentatives avant
   d'échouer. Ces retentatives ratées vident le seau sous la moitié.
2. **Budget épuisé.** Les appels 2 et 3 ne rapportent plus qu'une seule
   tentative chacun : la première tentative s'exécute, mais le budget refuse de
   réessayer. Le hook `OnRetryBudgetExceeded` se déclenche, et `Metrics()` ainsi
   que `HealthStatus()` font remonter la limitation — un état de santé dégradé
   qui laisse délibérément la disponibilité (readiness) intacte.
3. **Récupération.** Une série de 30 appels réussis remplit lentement le seau
   (0,1 jeton à la fois), repasse au-dessus de la moitié et efface la condition
   d'épuisement — les retentatives reprendraient à partir de là.

## Concepts clés

| Concept | Détail |
|---|---|
| `WithRetryBudget(MaxTokens, TokenRatio)` | Seau à jetons gouvernant le *taux* de retentatives : les échecs le vident, les succès le remplissent de `TokenRatio` à chaque fois |
| La première tentative s'exécute toujours | Le budget ne contrôle que les retentatives (à partir de la 2e tentative) ; les requêtes continuent de passer même quand le seau est vide |
| `r8e.Transient(err)` | Marque une erreur comme réessayable — seuls les échecs réessayables vident le budget |
| `OnRetryBudgetExceeded` | Hook déclenché chaque fois qu'une retentative est supprimée par le budget |
| `Metrics().RetryBudgetExceeded` / `RetryBudgetTokens` | Compteur des retentatives écartées et niveau de jetons en direct pour les tableaux de bord |
| Santé `retry_budget_exhausted` | Une condition *dégradée* qui ne contrôle jamais la disponibilité — le service est dégradé, pas hors service |

## Quand l'utiliser

- Tout client réessayant une dépendance susceptible d'échouer en masse, où des
  retentatives naïves multiplieraient la charge sur un service déjà en
  difficulté.
- À utiliser conjointement (et non à la place) des plafonds de retentatives par
  appel et du backoff — le budget borne le *taux* agrégé, tandis que le backoff
  espace les tentatives individuelles.
- Partagez un seul budget entre plusieurs politiques
  (`WithSharedRetryBudget`) pour un plafond de retentatives à l'échelle du
  processus ; agrégez sa jauge de jetons avec `max`/`avg`, pas `sum`, puisque
  chaque politique partageuse rapporte le même niveau.

## Exécution

```bash
go run ./examples/19-retry-budget/
```

## Sortie attendue

Trois sections. L'appel 1 effectue plusieurs tentatives ; les appels 2 et 3 ne
font qu'une seule tentative chacun, et `OnRetryBudgetExceeded` se déclenche. Le
bloc d'observabilité montre les retentatives supprimées, un faible nombre de
jetons et un état dégradé-mais-sain. Après récupération, le nombre de jetons est
revenu près de la capacité et la condition d'épuisement a disparu. Les nombres
exacts de tentatives sont déterministes ici, car le service en aval échoue
toujours immédiatement.
