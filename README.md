# VPS Control

Panel web open source pour administrer un VPS à distance — façon Pterodactyl,
mais généraliste (pas seulement des serveurs de jeu) : fichiers, services
Docker, déploiement d'applications, bases de données. Un seul binaire Go,
sans dépendance runtime lourde, pensé pour tourner confortablement sur un
**VPS à 2 Go de RAM**.

## Fonctionnalités de cette v1

- **Comptes admin propres au panel**, séparés des identifiants SSH/root du VPS. Premier compte créé lors de l'assistant d'installation initial. Multi-comptes (admin / lecture seule) gérables depuis l'interface.
- **Gestionnaire de fichiers web** : parcourir, éditer, uploader, télécharger, supprimer des fichiers/dossiers sans passer par SFTP.
- **Services Docker** : liste des conteneurs, start/stop/restart/suppression, consultation des logs, le tout par bouton.
- **Déploiement d'applications** :
  - depuis un **dépôt GitHub public** (`git clone` + build) ;
  - depuis un **fichier .zip** uploadé ;
  - détection automatique du stack (Laravel/PHP, Node.js, Python, site statique) et génération d'un `Dockerfile` si le projet n'en fournit pas déjà un ;
  - bouton pour créer rapidement une base MySQL ou PostgreSQL en conteneur à côté de l'app.
- **Navigateur de base de données** : connexion à une base MySQL/PostgreSQL (locale ou distante), liste des tables, visualisation des lignes, requêtes SQL libres.

## Architecture (pourquoi c'est léger)

- Backend en **Go**, compilé en un seul binaire (le HTML/CSS/JS est embarqué dedans via `go:embed`) → quelques dizaines de Mo de RAM en usage normal, pas de VM/JIT à faire tourner comme pour Node ou PHP.
- **Stockage JSON local** pour les comptes, connexions DB enregistrées et déploiements — pas de base de données à faire tourner pour le panel lui-même.
- **Docker** isole chaque application déployée ; c'est Docker qui gère le cycle de vie (start/stop/restart), le panel ne fait qu'appeler la CLI `docker`.
- Frontend en JS vanilla, aucun framework, aucun build step, aucune dépendance CDN.

## Installation sur votre VPS

```bash
# 1. Copiez ce dossier sur votre VPS (scp, git clone de votre propre dépôt, etc.)
scp -r vps-control root@VOTRE_IP:/opt/vpscontrol-src

# 2. Connectez-vous et lancez l'installation
ssh root@VOTRE_IP
cd /opt/vpscontrol
sudo bash scripts/install.sh
```

Le script installe Docker et Go si nécessaire, compile le binaire, crée les
dossiers de travail (`/opt/vpscontrol/data`, `/opt/vpscontrol/apps`) et
installe un service `systemd` qui démarre le panel automatiquement.

Par défaut, VPS Control écoute uniquement sur `127.0.0.1:8090` (pas exposé à
internet directement) — c'est volontaire pour vous forcer à mettre du HTTPS
devant. Deux options :

**Option A — tester rapidement via un tunnel SSH** (aucune config serveur) :
```bash
ssh -L 8090:localhost:8090 root@VOTRE_IP
# puis ouvrez http://localhost:8090 dans votre navigateur
```

**Option B — exposer proprement avec un nom de domaine (recommandé), via Caddy** (gère le HTTPS automatiquement) :
```bash
apt install -y caddy
cat >/etc/caddy/Caddyfile <<'EOF'
panel.votredomaine.com {
    reverse_proxy 127.0.0.1:8090
}
EOF
systemctl restart caddy
```
Pointez au préalable un enregistrement DNS A de `panel.votredomaine.com` vers l'IP du VPS.

À la première visite, un écran de configuration vous demande de créer le
premier compte admin.

## Variables d'environnement (modifiables dans `scripts/vpscontrol.service`)

| Variable | Par défaut | Rôle |
|---|---|---|
| `VPSCONTROL_LISTEN` | `127.0.0.1:8090` | Adresse d'écoute du panel |
| `VPSCONTROL_DATA_DIR` | `/opt/vpscontrol/data` | Comptes, connexions DB, déploiements (JSON) |
| `VPSCONTROL_DEPLOY_ROOT` | `/opt/vpscontrol/apps` | Dossier où sont clonées/extraites les apps déployées |
| `VPSCONTROL_FILES_ROOT` | `/home` | Dossier racine exposé par le gestionnaire de fichiers |

## Limites connues de cette v1 (honnêtes, pour la suite)

- **Un conteneur par app**, pas d'orchestration multi-conteneurs (pas de docker-compose généré automatiquement) — suffisant pour la plupart des petits projets, limitant pour des architectures plus complexes.
- Les **mots de passe de connexion aux bases de données** sont stockés en clair dans le fichier JSON local (permissions restreintes à root, mais pas chiffrés) — à améliorer avant un usage en production sensible.
- Pas encore de **build logs en direct** pendant un déploiement (l'appel API attend la fin du build avant de répondre) — pour un gros projet, le navigateur peut sembler figé une minute ou deux.
- Le **runtime tourne en root** pour pouvoir piloter Docker et parcourir les fichiers du système — c'est le même choix que fait Wings (l'agent de Pterodactyl), mais à documenter clairement pour vos utilisateurs.
- Pas de rôles fins par déploiement (un "viewer" voit tout en lecture, pas de scoping par projet).
- Le gestionnaire de fichiers édite du texte ; pas d'aperçu binaire/image intégré.

## Pistes pour la suite

- Logs de build en streaming (Server-Sent Events) pendant `docker build`.
- Chiffrement au repos des mots de passe de connexions DB.
- Templates docker-compose pour les stacks à plusieurs conteneurs (app + DB + cache).
- Webhooks GitHub pour redéployer automatiquement à chaque `push`.
- Historique des déploiements avec rollback vers une image précédente.
- Statistiques d'usage (CPU/RAM par conteneur) sur le tableau de bord.

## Structure du projet

```
vps-control/
├── main.go                    # routage HTTP, démarrage du serveur
├── internal/
│   ├── auth/                  # hash de mot de passe, sessions signées
│   ├── store/                 # stockage JSON (users, déploiements, connexions DB)
│   ├── middleware/             # auth middleware, helpers JSON
│   └── handlers/               # logique des endpoints API
├── web/                        # frontend (HTML/CSS/JS vanilla, embarqué dans le binaire)
├── scripts/
│   ├── install.sh              # installation automatique
│   └── vpscontrol.service      # unité systemd
└── go.mod
```

Licence à définir selon vos préférences pour l'open source (MIT ou Apache-2.0
sont les choix les plus courants pour ce type d'outil).
