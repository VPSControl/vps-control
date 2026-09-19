<p align="center">
  <img src="web/assets/logo-dark-bg.jpg" width="150" alt="VPS Control">
</p>

<h1 align="center">VPS Control</h1>

<p align="center">
  Un panel web open source pour administrer un VPS — dans l'esprit de Pterodactyl,
  mais généraliste : fichiers, services Docker, déploiements, bases de données.
</p>

<p align="center">
  <a href="https://vpscontrol.wazestudio.com">Site &amp; documentation</a> ·
  <a href="https://vpscontrol.wazestudio.com/docs.html">Guide d'installation</a> ·
  <a href="#licence">MIT</a>
</p>

---

## C'est quoi

J'en avais marre de faire les mêmes gestes manuels sur chaque VPS que je
montais : se connecter en SSH, chercher le bon dossier en SFTP, taper
`docker ps` puis `docker logs` puis `docker restart`, éditer un `.env` à la
main... VPS Control regroupe tout ça derrière une interface web unique,
avec ses propres comptes admin (rien à voir avec vos accès SSH/root).

Ce n'est pas un remplaçant de Kubernetes ou d'un vrai PaaS — c'est un outil
pour les gens qui gèrent un ou quelques VPS eux-mêmes et qui veulent y voir
plus clair sans y passer leur soirée.

## Fonctionnalités

- **Comptes admin propres au panel**, séparés du SSH/root. Multi-comptes
  (admin / lecture seule).
- **Gestionnaire de fichiers** dans le navigateur : parcourir, éditer,
  uploader, télécharger, supprimer — sans ouvrir de client SFTP.
- **Services Docker** : liste des conteneurs, start / stop / restart /
  suppression, logs, en cliquant sur un bouton plutôt qu'en tapant la
  commande.
- **Déploiement d'applications** depuis un dépôt GitHub public ou une
  archive `.zip`, avec détection automatique du stack (Laravel/PHP, Node.js,
  Python, site statique) et génération du `Dockerfile` si le projet n'en a
  pas déjà un.
- **Navigateur de base de données** : connexion MySQL/PostgreSQL, tables,
  lignes, requêtes SQL libres.
- **Mises à jour intégrées** : un bouton dans le panel (ou un minuteur
  quotidien, si activé) qui récupère la dernière version, recompile et
  redémarre.
- **HTTPS pris en charge à l'installation**, que vous ayez un nom de domaine
  (certificat Let's Encrypt via Certbot) ou non (certificat auto-signé,
  accès direct par IP).

## Pourquoi c'est léger

Le panel est un **unique binaire Go** — le frontend (HTML/CSS/JS vanilla,
aucun framework) est embarqué dedans via `go:embed`, donc rien à construire
côté Node. Les comptes, connexions aux bases et déploiements vivent dans un
simple fichier JSON local : pas de base de données à faire tourner pour le
panel lui-même. Chaque application déployée, elle, tourne dans son propre
conteneur Docker, isolée des autres.

R�sultat : ça tient confortablement sur un **VPS à 2 Go de RAM**, à côté des
applications que vous y déployez.

## Installation

Sur un VPS Debian/Ubuntu fraîchement installé :

```bash
curl -fsSL https://install.vpscontrol.wazestudio.com | sudo bash
```

Ou en clonant le dépôt vous-même :

```bash
git clone https://github.com/VPSControl/vps-control.git
cd vps-control
sudo bash scripts/install.sh
```

Le script installe Docker, Go et Nginx si nécessaire, compile le binaire, et
installe un service `systemd`. Il vous pose ensuite deux questions :

1. **Un nom de domaine, ou l'IP du VPS ?** Avec un domaine, Certbot obtient
   un certificat Let's Encrypt (il vous demandera lui-même votre email et
   l'acceptation des conditions, comme d'habitude). Sans domaine, laissez la
   réponse vide : le panel devient accessible en HTTPS directement sur
   l'IP du VPS et un port de votre choix (certificat auto-signé — votre
   navigateur affichera un avertissement la première fois, c'est normal).
2. **Activer les mises à jour automatiques quotidiennes ?** Sinon, un
   bouton dans l'onglet *Système* du panel fait la même chose à la demande.

À la première visite, un écran vous invite à créer le premier compte admin.

Le guide complet (variables d'environnement, architecture, limites
connues) est sur [vpscontrol.wazestudio.com/docs.html](https://vpscontrol.wazestudio.com/docs.html).

## Variables d'environnement

Modifiables dans `/etc/systemd/system/vpscontrol.service`, puis
`systemctl daemon-reload && systemctl restart vpscontrol`.

| Variable | Par défaut | Rôle |
|---|---|---|
| `VPSCONTROL_LISTEN` | `127.0.0.1:8090` | Adresse d'écoute du panel (Nginx fait le reverse proxy) |
| `VPSCONTROL_DATA_DIR` | `/opt/vpscontrol/data` | Comptes, connexions DB, déploiements (JSON) |
| `VPSCONTROL_DEPLOY_ROOT` | `/opt/vpscontrol/apps` | Dossier des applications déployées |
| `VPSCONTROL_FILES_ROOT` | `/home` | Racine exposée par le gestionnaire de fichiers |
| `VPSCONTROL_SRC_DIR` | `/opt/vpscontrol-src` | Dossier du code source, utilisé pour les mises à jour |
| `VPSCONTROL_REPO_URL` | `https://github.com/VPSControl/vps-control.git` | Dépôt d'origine, pour détecter les nouvelles versions |

## Structure du projet

```
vps-control/
├── main.go                     # routage HTTP, démarrage du serveur
├── internal/
│   ├── auth/                   # hash de mot de passe, sessions signées
│   ├── store/                  # stockage JSON (users, déploiements, connexions DB)
│   ├── middleware/              # auth middleware, helpers JSON
│   └── handlers/                # logique des endpoints API (fichiers, docker, deploy, db, système)
├── web/                         # frontend du panel (HTML/CSS/JS vanilla, embarqué dans le binaire)
├── scripts/
│   ├── install.sh               # installation (domaine/IP, HTTPS, mises à jour auto)
│   ├── update.sh                # git pull + rebuild + restart
│   ├── vpscontrol.service        # unité systemd du panel
│   └── vpscontrol-update.{service,timer}  # minuteur de mise à jour automatique
└── go.mod
```

Le site vitrine et l'installeur en une commande vivent dans un
[dépôt séparé](https://github.com/VPSControl/vpscontrol-website) — ce sont
de simples fichiers statiques, ils n'ont pas leur place à côté d'un projet Go.

## Limites connues (honnêtes, pour la suite)

- **Un conteneur par app** — pas d'orchestration multi-conteneurs
  automatique (pas de `docker-compose` généré), ce qui suffit pour la
  plupart des petits projets mais limite les architectures plus complexes.
- Les **mots de passe de connexion aux bases de données** sont stockés en
  clair dans le fichier JSON local (permissions restreintes à root, mais
  pas chiffrés).
- Pas encore de **logs de build en direct** — l'appel API attend la fin du
  build avant de répondre, donc le navigateur peut sembler figé une minute
  ou deux sur un gros projet.
- Le service **tourne en root** pour piloter Docker et parcourir les
  fichiers du système — même choix que Wings (l'agent de Pterodactyl), mais
  à avoir en tête.
- Pas de rôles fins par déploiement : un compte "lecture seule" voit tout,
  sans découpage par projet.

## Roadmap

- Logs de build en streaming (Server-Sent Events) pendant `docker build`
- Chiffrement au repos des mots de passe de connexions DB
- Templates docker-compose pour les stacks multi-conteneurs (app + DB + cache)
- Webhooks GitHub pour redéployer automatiquement à chaque push
- Historique des déploiements avec rollback
- Statistiques CPU/RAM par conteneur sur le tableau de bord

## Contribuer

Les issues et pull requests sont bienvenues sur
[GitHub](https://github.com/VPSControl/vps-control). Pas de process
compliqué : ouvrez une issue si vous voulez discuter d'un changement avant
de vous lancer.

## Licence

MIT — voir [`LICENSE`](LICENSE).
