# Yumlab

Scanner CLI qui analyse les workflows GitHub Actions et détecte ce qui va faire
échouer un pipeline **avant** de le pousser.

---

## Contexte produit

**Le problème :** un pipeline tourne 20 minutes et casse à la dernière étape.
Un secret manquant, une permission de token absente, un cache mal configuré.
Le dev corrige, recommite, réattend 20 minutes. Répéter jusqu'à ce que ce soit vert.

**La solution :** un binaire qui lit `.github/workflows/*.yml` + l'API GitHub,
et sort en quelques secondes la liste de ce qui va casser, triée par
**minutes gaspillées estimées**.

**Public :** devs et DevOps qui maintiennent leurs propres pipelines.
Adoption individuelle par le CLI, conversion par l'équipe.

### Ce que Yumlab N'EST PAS

Ces directions ont été explicitement écartées après recherche marché.
Ne jamais les proposer, même si elles semblent naturelles :

- ❌ **Un builder visuel / drag & drop de YAML.** Marché mort (Actionforge,
  FlowForge, githubworkflow.com — tous gratuits ou abandonnés). Personne ne se
  plaint d'écrire du YAML depuis que l'IA le génère.
- ❌ **Un exécuteur de pipelines hébergé.** C'est vendre du compute, marché
  saturé et capitalistique (Blacksmith, Depot, RunsOn, Ubicloud).
- ❌ **Un runner local / concurrent d'`act`.** Reproduire fidèlement un runner
  GitHub est un problème non résolu après des années. Hors scope.
- ❌ **Un scanner de sécurité.** Déjà occupé (Plumber, StepSecurity, zizmor).
  L'angle Yumlab est le **temps perdu**, pas la sécurité. Un contrôle peut avoir
  un effet de bord sécurité, mais le message reste : "ça va te faire perdre X minutes".
- ❌ **Un classificateur d'échecs post-mortem.** Yumlab agit *avant* le push.
  Analyser un build rouge pour dire à qui la faute est un autre produit (cf. Koredex).
- ❌ **Une plateforme d'apprentissage CI/CD.**

### Contraintes structurantes

- **Lecture seule.** Yumlab n'écrit jamais dans un repo, ne crée pas de PR,
  ne modifie aucun workflow. (Réévaluable en v2, pas avant.)
- **Pas de backend.** Pas de base de données, pas de compte, pas d'OAuth,
  pas de serveur. Le binaire tourne sur la machine du dev ou dans son CI,
  avec son token. Toute PR qui introduit un serveur est hors scope.
- **Pas de télémétrie.** Aucun appel réseau autre que l'API GitHub.

---

## Principes de conception

Ces trois principes priment sur la couverture fonctionnelle. Dans le doute,
c'est eux qui tranchent.

### 1. Aucun LLM dans la logique de détection

Les contrôles sont des règles déterministes, lisibles et auditables par
l'utilisateur. Un LLM qui a l'air sûr et un LLM qui a raison se ressemblent
dans une sortie de terminal. Un LLM peut éventuellement servir à *reformuler*
un message d'aide, jamais à décider s'il y a un problème.

### 2. INCONNU est une réponse de premier plan

Quand Yumlab ne peut pas vérifier quelque chose — permission manquante,
expression trop dynamique, cas non couvert — il le dit explicitement et
compte le nombre de références non vérifiées. Il ne devine pas.

Exemple de sortie attendue :
`3 références de secrets non vérifiées (permission organisation manquante)`

### 3. Zéro faux positif toléré

Un faux "il te manque un secret" alors que le secret existe détruit la
confiance définitivement. C'est l'erreur la plus coûteuse du produit, bien
plus qu'un contrôle manqué. Dans le doute : INCONNU, jamais un finding.

---

## Permissions GitHub — contrainte critique

**C'est le principal risque d'adoption du projet.** Lister les secrets d'un
repo demande la permission "Secrets" en lecture, ce qui suppose en pratique
un accès admin sur le repo. Pour les secrets d'organisation, il faut le scope
`admin:org` (PAT classique) ou la permission "Secrets" au niveau organisation
(token fine-grained). L'utilisateur type — un dev sur un repo de sa boîte —
n'a ni l'un ni l'autre.

Conséquences non négociables :

- **Mode dégradé obligatoire.** Si le token ne peut pas lire un niveau
  (repo / environnement / organisation), Yumlab ne signale rien pour ce niveau
  et l'annonce clairement. Jamais de finding "secret manquant" faute de droits.
- **Fallback déclaratif.** `.yumlab.yaml` permet de déclarer manuellement les
  noms de secrets connus au niveau org, ce qui débloque le contrôle sans admin.
- **Le mode CI est le chemin nominal.** Dans un workflow, le contexte
  d'exécution est meilleur et l'autorisation a été donnée une fois par l'admin
  pour toute l'équipe. Le mode local est le mode découverte.

Les permissions requises doivent être documentées **par contrôle**, avec ce qui
se dégrade quand elles manquent. Priorité aux tokens fine-grained, PAT classique
en secondaire.

Lire les fichiers de workflow ne demande aucun token : ils sont sur le disque.
Le token ne sert qu'aux secrets, variables, environnements et historique de runs.

---

## Stack

- **Go 1.23+** — binaire statique unique, cross-compilé, zéro runtime.
  Indispensable pour un outil qui doit tourner dans n'importe quel conteneur CI.
- `github.com/google/go-github` — client API GitHub officiel.
- `gopkg.in/yaml.v3` — parsing en mode `yaml.Node` **obligatoire**, pour
  conserver ligne et colonne de chaque nœud. Chaque finding doit pointer un
  `fichier:ligne`, sinon il est inutilisable.
- `github.com/spf13/cobra` — commandes CLI.
- `goreleaser` — build multi-plateforme, Homebrew, image Docker.

Pas de framework web. Pas d'ORM. Pas de dépendance qui tire un runtime.

### Le morceau difficile

Les expressions `${{ ... }}` de GitHub Actions ne sont pas du YAML — il faut
un mini-parser dédié pour en extraire les références (`secrets.X`, `vars.X`,
`env.X`, `matrix.X`, `needs.X.outputs.Y`). C'est le cœur technique du projet.
Le construire tôt, dans son propre package, avec une grosse couverture de tests.

Une expression trop dynamique pour être résolue statiquement
(`secrets[format('{0}_KEY', env.REGION)]`) retourne INCONNU, pas un finding.

---

## Architecture

```
cmd/yumlab/          entrée CLI (cobra)
internal/parse/      workflows YAML -> AST interne, avec positions
internal/expr/       parser d'expressions ${{ }} et extraction de références
internal/github/     client API (secrets, vars, environnements, historique runs)
internal/controls/   un fichier par contrôle, interface commune
internal/report/     rendu terminal, JSON, SARIF
internal/score/      calcul du score et estimation des minutes gaspillées
docs/controls/       une page de documentation par contrôle
testdata/            workflows d'exemple, cassés et sains
```

Chaque contrôle implémente la même interface et est activable/désactivable
indépendamment. Ajouter un contrôle ne doit toucher aucun autre package.

### Format d'un finding

Tout contrôle retourne : identifiant du contrôle, sévérité, `fichier:ligne`,
message court, explication, et **minutes gaspillées estimées**. Ce dernier
champ n'est pas optionnel — c'est ce qui rend le rapport partageable à un lead.

Un contrôle retourne aussi son **niveau de couverture** : ce qu'il a pu
vérifier et ce qu'il n'a pas pu, pour alimenter la sortie INCONNU.

### Deux familles de contrôles

Distinction structurante, à respecter dès le premier contrôle : chaque contrôle
déclare s'il a besoin du réseau ou non.

- **Statiques (hors ligne)** — cache mort, bloc `permissions` absent, PAT en
  dur. Aucun appel API, exécution instantanée. Ce sont les seuls utilisables
  dans un hook pre-commit.
- **Réseau** — existence des secrets et variables, historique des runs.
  Utilisables en local à la demande et en CI, jamais dans un hook.

Le CLI doit pouvoir tourner en mode statique seul (`--offline`), sans token et
sans aucun appel sortant.

---

## Roadmap

### v0.1 — Le contrôle signature

Objectif : prouver la valeur avec un seul contrôle. Deux week-ends max.

- Commande `yumlab scan` sur le repo courant
- Auth par `GITHUB_TOKEN` en variable d'environnement, rien d'autre
- **Contrôle 1 — Secrets fantômes** : toute référence `secrets.X` / `vars.X`
  dans un workflow, confrontée à ce qui existe réellement (niveau repo,
  environnement, organisation). Gérer les environnements : un secret peut
  exister pour `production` et pas pour `staging`.
- Mode dégradé complet sur les permissions manquantes, dès la v0.1
- Sortie terminal lisible, avec `fichier:ligne`
- Code de sortie non nul si au moins un finding
- Documentation du contrôle et page "token & permissions"

Livrable : binaire installable, README avec un avant/après.

### v0.2 — Permissions et credentials

- **Contrôle 2 — Permissions du token** : bloc `permissions:` absent (token
  large par défaut), ou décalage entre ce que le job fait réellement (push
  d'image, commentaire de PR, création de release) et ce qu'il a le droit de
  faire. Signaler dans les deux sens : trop **et** pas assez.
- **Contrôle 3 — Credentials long-lived** : PAT stocké en secret là où OIDC
  ou un token d'installation GitHub App conviendrait.

### v0.3 — Cache

- **Contrôle 4 — Cache mort** : clé sans hash du lockfile, `restore-keys`
  absent, cache écrit mais jamais restauré, dépendances réinstallées dans
  chaque job d'un même workflow.

C'est la douleur la plus votée dans les retours communauté. L'estimation de
minutes gaspillées doit être particulièrement soignée ici.

### v0.4 — Historique et score

- **Contrôle 5 — Ordre d'échec** : via l'API des runs, identifier le job qui
  casse le plus souvent. S'il est positionné tard alors qu'il pourrait être
  tôt, le signaler avec les minutes gaspillées cumulées.
- Score global agrégé
- Sorties `--json` et `--sarif`
- Liste des findings triée par minutes gaspillées, pas par sévérité

### v0.5 — Distribution

- GitHub Action officielle : 3 lignes dans un workflow, rapport dans les logs
- **Hook pre-commit** : `.pre-commit-hooks.yaml` à la racine, le framework
  supporte nativement les binaires Go. Le hook n'exécute que les contrôles
  statiques (`--offline`) — un hook qui appelle le réseau, prend 3 secondes et
  échoue dans le train est un hook qu'on désactive. C'est la famille d'outils
  dans laquelle Yumlab se range : actionlint, zizmor, gitleaks, ruff.
- Image Docker, formule Homebrew, `go install`

Ce sont des canaux, pas des fonctionnalités : ils font passer l'outil d'un dev
à son équipe. Le hook n'a d'intérêt qu'une fois les contrôles statiques livrés
(v0.2 et v0.3), pas avant.

Piste pour plus tard, pas en v0.5 : mettre en cache sur disque la liste des
**noms** de secrets (quelques heures de validité) pour rendre le contrôle
secrets utilisable hors ligne. Jamais de valeurs, uniquement des noms, et
documenté explicitement.

### v1.0 — Stabilisation

- Fichier `.yumlab.yaml` : activer/désactiver les contrôles, ignorer des
  chemins, déclarer les secrets org connus, définir un seuil de score
- Le seuil fait échouer le job (gate local, sans serveur)
- Documentation complète par contrôle
- Gel de l'API des contrôles

### v2.0 — GitLab CI

Support de `.gitlab-ci.yml` avec résolution des `include:`. C'est le vrai
avantage différenciant du projet — quasiment personne ne couvre les deux
plateformes correctement. Mais inutile tant que la v1 n'a pas d'utilisateurs.
Ne pas commencer avant.

### Plus tard — seulement si traction

Vue organisation multi-repos, historique de score, politiques d'équipe,
commentaires automatiques en PR. C'est la partie payante, et la seule qui
justifiera un backend et une GitHub App avec OAuth. Rien de tout ça avant
d'avoir des installs qui se comptent en centaines.

---

## Documentation

La doc n'est pas un livrable de fin de projet : sans elle, personne ne peut
créer le bon token, donc personne n'utilise l'outil.

- **Page "token & permissions"** — tableau des permissions requises par
  contrôle, en fine-grained d'abord, PAT classique ensuite, et ce qui se
  dégrade quand une permission manque. Page la plus importante du site.
- **Une page par contrôle** — pourquoi c'est un problème, combien de temps ça
  coûte, comment corriger, comment l'ignorer légitimement.
- **Repo de démonstration** — un repo public avec un workflow délibérément
  cassé par contrôle, pour que les gens vérifient les affirmations au lieu de
  faire confiance. C'est aussi le meilleur support de lancement.

README et documentation en anglais, discussions internes en français.

---

## Positionnement et objection attendue

L'objection récurrente dans la communauté DevOps, sur tous les fils analysés :
*"si ton CI est rouge en permanence, le problème ce sont tes pratiques de dev,
pas ton CI."*

La bonne réponse n'est pas de débattre, c'est de segmenter : si chez toi le
rouge veut toujours dire un vrai échec de ton code, tu n'es pas l'utilisateur
de Yumlab, et tant mieux. Yumlab vise les repos où l'échec vient d'une
configuration invérifiable avant le push.

Cette formulation doit apparaître dans le README et dans tout post de lancement.

---

## Modèle économique et licence

- **Licence Apache 2.0.** Maximise l'adoption et rassure les entreprises, tout
  en laissant la partie serveur propriétaire le moment venu.
- **Le CLI reste gratuit et complet, définitivement.** Y compris le gate local
  par seuil de score. Aucune fonctionnalité déjà livrée en open source ne sera
  déplacée derrière un paiement — c'est la garantie qui tient la communauté.
- **Ce qui sera payant** est ce que le CLI ne peut structurellement pas faire :
  agrégation multi-repos, historique de score, politique imposée au niveau
  organisation. La frontière est technique, pas arbitraire.
- Gratuit à vie sur les repos publics.

Ne rien construire de tout ça avant d'avoir des utilisateurs réels.

---

## Conventions

- Erreurs enveloppées avec `fmt.Errorf("...: %w", err)`, jamais ignorées
- Chaque contrôle a des cas de test avec des workflows réels dans `testdata/`,
  cassés et sains
- Messages de findings courts et actionnables : quoi, où, comment corriger
- Toute nouvelle règle de détection doit être explicable en une phrase à un
  utilisateur ; si elle ne l'est pas, elle est trop floue pour être fiable