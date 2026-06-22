*[Read in English](README.md)*

# Exemple 24 — Cache en lecture directe

Illustre `WithCache`, la politique de cache en lecture directe (read-through) qui
regroupe quatre comportements derrière une seule option, afin qu'une clé populaire
ne transforme pas chaque requête en aller-retour vers le downstream.

## Ce que cet exemple illustre

Le cache est indexé sur le contexte d'appel (le même idiome que la coalescence de
requêtes), stocke les valeurs sous forme d'enveloppes `r8e.CacheEntry[T]`, et
mesure la fraîcheur par rapport à l'horloge (`Clock`) de la politique. Un minuscule
`mapCache` en mémoire remplace ici un véritable adaptateur otter ou ristretto.
L'exemple parcourt quatre sections, chacune réinitialisant le compteur d'appels au
backend pour montrer précisément quand le downstream a été sollicité :

1. **Lecture directe** — le premier appel manque le cache et le peuple ; le second
   tombe dans le TTL de fraîcheur de 50 ms et est servi depuis le cache, donc le
   backend n'est appelé qu'une fois.
2. **ForceRefresh** — `r8e.ForceRefresh(ctx)` renvoie un contexte enfant qui fait
   ignorer la lecture en cache pour un appel et repeuple en cas de succès — la
   porte de sortie quand il faut la valeur faisant autorité, maintenant.
3. **Stale-if-error** — une fois la valeur vieillie au-delà du TTL de fraîcheur et
   le backend en panne, la revalidation en échec sert la **dernière valeur
   correcte connue** au lieu de l'erreur (RFC 5861 stale-if-error) : une brève
   panne se dégrade en données légèrement périmées.
4. **Cache négatif** — une clé n'ayant jamais réussi n'a aucune valeur périmée de
   repli, son échec est donc mis en cache brièvement ; l'appel suivant échoue vite
   depuis cette entrée négative au lieu de marteler le backend en panne.

Enfin, il affiche les métriques du cache (`hits`, `misses`, `stores`,
`stale_served`).

## Concepts clés

| Concept | Détail |
|---|---|
| `WithCache(cache, key, ttl, ...)` | Cache en lecture directe ; un hit frais court-circuite toute la chaîne, un miss déroule la chaîne et met en cache un résultat réussi |
| Fonction de clé sur le contexte | La clé provient du contexte d'appel (`resourceKey`), le même idiome pilote donc cache et coalescence ; une clé vide exclut un appel |
| `r8e.CacheEntry[T]` | L'enveloppe stockée par le cache, portant l'âge de chaque entrée et toute erreur enregistrée pour distinguer frais / périmé / négatif |
| `StaleIfError(d)` | Au-delà du TTL de fraîcheur, une revalidation en échec sert la valeur périmée pendant `d` au lieu d'une erreur, déclenchant `OnStaleServed` |
| `NegativeCache(d)` | Un échec sans repli périmé est lui-même mis en cache pendant `d`, donc une clé connue mauvaise échoue vite |
| `ForceRefresh(ctx)` | Contexte enfant qui ignore la lecture en cache pour un appel et repeuple en cas de succès |

## Quand l'utiliser

- Lectures intensives de dépendances lentes ou limitées en débit où les mêmes clés
  reviennent (catalogues, profils, configuration) — transformer des lectures
  répétées en hits bon marché.
- Quand servir des données légèrement périmées pendant une brève panne vaut mieux
  qu'une erreur (stale-if-error), ou quand des clés connues mauvaises doivent
  cesser de marteler le backend (cache négatif).
- Associez `WithCoalesce(key)` + `WithTimeout` pour aussi réduire une ruée de miss
  concurrents en un seul appel downstream (voir l'exemple 20).

## Exécution

```bash
go run ./examples/24-read-through-cache/
```

## Sortie attendue

Quatre sections étiquetées. La lecture directe indique que le backend a été appelé
une fois (le second était un hit de cache) ; ForceRefresh montre un appel au
backend ; stale-if-error sert la valeur précédente pendant que le backend est en
panne ; le cache négatif fait échouer vite le second appel sans toucher le backend.
La ligne de métriques finale résume l'exécution.
