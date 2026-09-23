#!/usr/bin/env bash
#
# VPS CTL — installation script
#
# Usage:
#   sudo bash scripts/install.sh
#
# Non-interactive:
#   VPSCONTROL_DOMAIN=panel.example.com sudo -E bash scripts/install.sh
#   VPSCONTROL_AUTO_UPDATE=yes sudo -E bash scripts/install.sh
#
set -euo pipefail

# =====================================================================
# Couleurs & helpers
# =====================================================================
if [ -t 1 ]; then
  RED=$'\033[0;31m'
  GREEN=$'\033[0;32m'
  YELLOW=$'\033[0;33m'
  BLUE=$'\033[0;34m'
  CYAN=$'\033[0;36m'
  BOLD=$'\033[1m'
  DIM=$'\033[2m'
  NC=$'\033[0m'
else
  RED=""; GREEN=""; YELLOW=""; BLUE=""; CYAN=""; BOLD=""; DIM=""; NC=""
fi

log()    { printf '%s==>%s %s\n' "$BLUE" "$NC" "$*"; }
ok()     { printf '%s✓%s %s\n' "$GREEN" "$NC" "$*"; }
warn()   { printf '%s!%s %s\n' "$YELLOW" "$NC" "$*"; }
err()    { printf '%s✗%s %s\n' "$RED" "$NC" "$*" >&2; }
step()   { printf '\n%s%s▶ %s%s\n' "$BOLD" "$CYAN" "$*" "$NC"; }

print_banner() {
  local G=$'\033[38;5;46m'
  local D=$'\033[38;5;28m'
  local N=$'\033[0m'
  if [ ! -t 1 ]; then G=""; D=""; N=""; fi

  printf '\n'
  printf '%s   ██╗   ██╗██████╗ ███████╗     ██████╗████████╗██╗     %s\n' "$G" "$N"
  printf '%s   ██║   ██║██╔══██╗██╔════╝    ██╔════╝╚══██╔══╝██║     %s\n' "$G" "$N"
  printf '%s   ██║   ██║██████╔╝███████╗    ██║        ██║   ██║     %s\n' "$G" "$N"
  printf '%s   ╚██╗ ██╔╝██╔═══╝ ╚════██║    ██║        ██║   ██║     %s\n' "$G" "$N"
  printf '%s    ╚████╔╝ ██║     ███████║    ╚██████╗   ██║   ███████╗%s\n' "$G" "$N"
  printf '%s     ╚═══╝  ╚═╝     ╚══════╝     ╚═════╝   ╚═╝   ╚══════╝%s\n' "$G" "$N"
  printf '\n'
  printf '%s           VPS Control — installation%s\n' "$D" "$N"
  printf '%s           ────────────────────────────%s\n' "$D" "$N"
  printf '\n'
}

print_mini_banner() {
  local G=$'\033[38;5;46m'
  local D=$'\033[38;5;28m'
  local N=$'\033[0m'
  if [ ! -t 1 ]; then G=""; D=""; N=""; fi
  printf '%s╭─ VPS CTL%s%s ─╮%s\n' "$G" "$D" "$N" "$NC"
}

# =====================================================================
# Vérifications préalables
# =====================================================================
if [ "$(id -u)" -ne 0 ]; then
  err "This script must be run as root (sudo bash scripts/install.sh)"
  exit 1
fi

print_banner

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR"

if [ ! -f "main.go" ] || [ ! -f "go.mod" ]; then
  err "Ce script doit être lancé depuis le dépôt vps-control."
  err "Dossier courant : $PROJECT_DIR"
  exit 1
fi

ok "Dépôt détecté : $PROJECT_DIR"

# =====================================================================
# Helper interactif
# =====================================================================
ask() {
  local prompt="$1" default="$2" var
  if [ -r /dev/tty ]; then
    printf '%s%s%s [%s%s%s] ' "$BOLD" "$prompt" "$NC" "$DIM" "$default" "$NC" > /dev/tty
    read -r var </dev/tty || true
  fi
  echo "${var:-$default}"
}

# =====================================================================
# Questions de configuration
# =====================================================================
step "Configuration"

DOMAIN="${VPSCONTROL_DOMAIN:-}"
if [ -z "$DOMAIN" ]; then
  DOMAIN="$(ask "Nom de domaine (vide = accès par IP) :" "")"
fi

if [ -n "$DOMAIN" ]; then
  DOMAIN="$(echo "$DOMAIN" | sed -E 's~^https?://~~; s~/.*$~~; s~[[:space:]]~~g')"
  ok "Domaine : $DOMAIN"
else
  warn "Pas de domaine — accès par IP + port HTTPS auto-signé"
fi

AUTO_UPDATE="${VPSCONTROL_AUTO_UPDATE:-}"
if [ -z "$AUTO_UPDATE" ]; then
  AUTO_UPDATE="$(ask "Activer les mises à jour automatiques quotidiennes ? (y/N) :" "n")"
fi

# =====================================================================
# 1/6 — Paquets de base
# =====================================================================
step "1/6 · Paquets de base"

log "apt-get update..."
apt-get update -y >/dev/null 2>&1

log "Installation : ca-certificates, curl, git, gnupg, nginx, openssl, tar..."
apt-get install -y ca-certificates curl git gnupg nginx openssl tar >/dev/null 2>&1

if command -v nginx >/dev/null 2>&1; then
  ok "nginx $(nginx -v 2>&1 | awk -F/ '{print $2}')"
else
  err "Échec d'installation de nginx"
  exit 1
fi
if command -v git >/dev/null 2>&1; then
  ok "git $(git --version | awk '{print $3}')"
fi
if command -v openssl >/dev/null 2>&1; then
  ok "openssl $(openssl version | awk '{print $2}')"
fi

# =====================================================================
# 2/6 — Docker
# =====================================================================
step "2/6 · Docker"

if ! command -v docker >/dev/null 2>&1; then
  log "Docker absent — installation via get.docker.com..."
  if ! curl -fsSL https://get.docker.com | sh >/dev/null 2>&1; then
    err "Échec de l'installation de Docker"
    exit 1
  fi
  ok "Docker installé"
else
  ok "Docker déjà présent ($(docker --version | awk '{print $3}' | tr -d ','))"
fi

systemctl enable --now docker >/dev/null 2>&1
if systemctl is-active --quiet docker; then
  ok "Service docker actif"
else
  err "Le service docker n'a pas démarré"
  exit 1
fi

# =====================================================================
# 3/6 — Go
# =====================================================================
step "3/6 · Go toolchain"

if ! command -v go >/dev/null 2>&1; then
  log "Go absent — installation..."
  GO_VERSION="1.22.5"
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64)  GOARCH="amd64" ;;
    aarch64) GOARCH="arm64" ;;
    armv7l)  GOARCH="armv6l" ;;
    *)
      err "Architecture non supportée : $ARCH"
      exit 1
      ;;
  esac
  if ! curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz; then
    err "Échec du téléchargement de Go"
    exit 1
  fi
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
  rm -f /tmp/go.tar.gz
  ok "Go ${GO_VERSION} installé"
else
  ok "Go déjà présent ($(go version | awk '{print $3}'))"
fi

# =====================================================================
# 4/6 — Build
# =====================================================================
step "4/6 · Compilation de VPS Control"

log "Résolution des dépendances (go mod tidy)..."
go mod tidy >/dev/null 2>&1 || true

log "Compilation du binaire..."
if ! go build -o /usr/local/bin/vpscontrol . ; then
  err "Échec de la compilation. Vérifie les erreurs ci-dessus."
  exit 1
fi
chmod +x /usr/local/bin/vpscontrol
SIZE="$(du -h /usr/local/bin/vpscontrol | awk '{print $1}')"
ok "Binaire installé : /usr/local/bin/vpscontrol (${SIZE})"

# =====================================================================
# 5/6 — Dossiers + service systemd
# =====================================================================
step "5/6 · Service systemd & dossiers"

mkdir -p /opt/vpscontrol/data
mkdir -p /opt/vpscontrol/apps
mkdir -p /opt/vpscontrol/backups
chmod 700 /opt/vpscontrol/data
chmod 700 /opt/vpscontrol/backups
chmod 755 /opt/vpscontrol/apps
ok "Données    : /opt/vpscontrol/data"
ok "Apps       : /opt/vpscontrol/apps"
ok "Backups    : /opt/vpscontrol/backups"

REPO_ORIGIN="$(git -C "$PROJECT_DIR" remote get-url origin 2>/dev/null || echo "https://github.com/VPSControl/vps-control.git")"

cat > /etc/systemd/system/vpscontrol.service <<EOF
[Unit]
Description=VPS Control panel
After=network.target docker.service
Requires=docker.service

[Service]
Type=simple
ExecStart=/usr/local/bin/vpscontrol
Restart=on-failure
RestartSec=3

Environment=VPSCONTROL_DATA_DIR=/opt/vpscontrol/data
Environment=VPSCONTROL_DEPLOY_ROOT=/opt/vpscontrol/apps
Environment=VPSCONTROL_BACKUP_ROOT=/opt/vpscontrol/backups
Environment=VPSCONTROL_FILES_ROOT=/home
Environment=VPSCONTROL_LISTEN=127.0.0.1:8090
Environment=VPSCONTROL_SRC_DIR=${PROJECT_DIR}
Environment=VPSCONTROL_REPO_URL=${REPO_ORIGIN}

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable vpscontrol >/dev/null 2>&1
systemctl restart vpscontrol
sleep 1

if systemctl is-active --quiet vpscontrol; then
  ok "Service vpscontrol activé et démarré"
else
  err "Le service vpscontrol n'est pas actif :"
  journalctl -u vpscontrol -n 20 --no-pager
  exit 1
fi

# =====================================================================
# 6/6 — Nginx & HTTPS
# =====================================================================
step "6/6 · Nginx & HTTPS"

# Désactiver le site par défaut
rm -f /etc/nginx/sites-enabled/default

ACCESS_URL=""

if [ -n "$DOMAIN" ]; then
  # =====================================================================
  # Cas 1 : avec domaine
  # =====================================================================
  log "Configuration Nginx pour ${DOMAIN}..."

  # Config HTTP uniquement (Certbot ajoutera le SSL).
  # client_max_body_size 30m : autorise les uploads ZIP jusqu'à 30 MB.
  cat > /etc/nginx/sites-available/vpscontrol.conf <<NGINX
server {
    listen 80;
    listen [::]:80;
    server_name ${DOMAIN};

    client_max_body_size 30m;

    location / {
        proxy_pass http://127.0.0.1:8090;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
NGINX

  ln -sf /etc/nginx/sites-available/vpscontrol.conf /etc/nginx/sites-enabled/vpscontrol.conf

  if ! nginx -t >/dev/null 2>&1; then
    err "La config nginx est invalide :"
    nginx -t
    exit 1
  fi
  systemctl reload nginx
  ok "Nginx configuré (HTTP, en attente du certificat)"

  # Firewall
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw allow 80/tcp >/dev/null 2>&1 || true
    ufw allow 443/tcp >/dev/null 2>&1 || true
    ok "UFW : ports 80 et 443 ouverts"
  fi

  # Certbot
  if ! command -v certbot >/dev/null 2>&1; then
    log "Installation de Certbot..."
    apt-get install -y certbot python3-certbot-nginx >/dev/null 2>&1
  fi

  if [ -r /dev/tty ]; then
    printf '\n%sCertbot va demander un email et l''acceptation des CGU.%s\n' "$DIM" "$NC"
    printf '%sAssure-toi que le DNS de %s pointe bien vers ce serveur.%s\n\n' "$DIM" "$DOMAIN" "$NC"

    if certbot --nginx -d "$DOMAIN" --redirect --agree-tos --register-unsafely-without-email </dev/tty; then
      ok "Certificat Let's Encrypt obtenu pour ${DOMAIN}"
      # Certbot réécrit la conf : on s'assure que client_max_body_size
      # est toujours présent (le plugin --nginx conserve les directives
      # du bloc server, mais on vérifie par sécurité).
      if ! grep -q "client_max_body_size" /etc/nginx/sites-available/vpscontrol.conf; then
        warn "client_max_body_size manquant après Certbot, ajout automatique..."
        sed -i '/server_name/a\    client_max_body_size 30m;' /etc/nginx/sites-available/vpscontrol.conf
        nginx -t && systemctl reload nginx
      fi
    else
      warn "Certbot n'a pas terminé automatiquement."
      warn "Relance manuellement : sudo certbot --nginx -d ${DOMAIN}"
      warn "Vérifie aussi que : dig ${DOMAIN} +short pointe vers ce serveur"
    fi
  else
    warn "Pas de terminal interactif — Certbot ignoré."
    warn "Lance manuellement : sudo certbot --nginx -d ${DOMAIN}"
  fi

  # Vérifier la config finale après Certbot
  if nginx -t >/dev/null 2>&1; then
    systemctl reload nginx
  else
    err "Nginx configuré par Certbot est invalide :"
    nginx -t
  fi

  ACCESS_URL="https://${DOMAIN}"

else
  # =====================================================================
  # Cas 2 : sans domaine (IP + port HTTPS auto-signé)
  # =====================================================================
  PORT="$(ask "Port HTTPS pour l'accès par IP :" "8443")"

  PUBLIC_IP="$(curl -4 -fsSL --max-time 5 ifconfig.me 2>/dev/null || hostname -I | awk '{print $1}')"

  log "Génération d'un certificat auto-signé pour ${PUBLIC_IP}..."
  mkdir -p /etc/vpscontrol/ssl
  if openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
    -keyout /etc/vpscontrol/ssl/selfsigned.key \
    -out /etc/vpscontrol/ssl/selfsigned.crt \
    -subj "/CN=${PUBLIC_IP}" >/dev/null 2>&1; then
    chmod 600 /etc/vpscontrol/ssl/selfsigned.key
    ok "Certificat auto-signé créé (valide 825 jours)"
  else
    err "Échec de la génération du certificat"
    exit 1
  fi

  # client_max_body_size 30m : autorise les uploads ZIP jusqu'à 30 MB.
  cat > /etc/nginx/sites-available/vpscontrol.conf <<NGINX
server {
    listen ${PORT} ssl;
    listen [::]:${PORT} ssl;
    server_name _;

    client_max_body_size 30m;

    ssl_certificate     /etc/vpscontrol/ssl/selfsigned.crt;
    ssl_certificate_key /etc/vpscontrol/ssl/selfsigned.key;

    location / {
        proxy_pass http://127.0.0.1:8090;
        proxy_http_version 1.1;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
        proxy_set_header Upgrade \$http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
NGINX

  ln -sf /etc/nginx/sites-available/vpscontrol.conf /etc/nginx/sites-enabled/vpscontrol.conf

  if ! nginx -t >/dev/null 2>&1; then
    err "La config nginx est invalide :"
    nginx -t
    exit 1
  fi
  systemctl reload nginx
  ok "Nginx configuré sur le port ${PORT}"

  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -q "Status: active"; then
    ufw allow "${PORT}/tcp" >/dev/null 2>&1 || true
    ok "UFW : port ${PORT} ouvert"
  fi

  ACCESS_URL="https://${PUBLIC_IP}:${PORT}"
fi

# =====================================================================
# Mises à jour automatiques (optionnel)
# =====================================================================
step "Mises à jour automatiques"

case "${AUTO_UPDATE,,}" in
  y|yes)
    if [ -f "$PROJECT_DIR/scripts/update.sh" ]; then
      cat > /etc/systemd/system/vpscontrol-update.service <<EOF
[Unit]
Description=VPS Control auto-update
After=network.target

[Service]
Type=oneshot
ExecStart=/bin/bash ${PROJECT_DIR}/scripts/update.sh
EOF

      cat > /etc/systemd/system/vpscontrol-update.timer <<'EOF'
[Unit]
Description=Run VPS Control auto-update daily

[Timer]
OnCalendar=daily
RandomizedDelaySec=30min
Persistent=true

[Install]
WantedBy=timers.target
EOF

      systemctl daemon-reload
      systemctl enable --now vpscontrol-update.timer >/dev/null 2>&1
      ok "Timer quotidien activé"
      AUTO_UPDATE_MSG="Activé (une fois par jour, aléatoirement dans les 30 min)"
    else
      warn "scripts/update.sh introuvable — mises à jour auto désactivées"
      AUTO_UPDATE_MSG="Indisponible (script manquant)"
    fi
    ;;
  *)
    ok "Mises à jour auto désactivées"
    AUTO_UPDATE_MSG="Désactivé"
    ;;
esac

# =====================================================================
# Vérification finale
# =====================================================================
step "Vérification finale"

sleep 1

# 1. Service actif
if systemctl is-active --quiet vpscontrol; then
  ok "Service vpscontrol actif"
else
  err "Le service vpscontrol n'est pas actif :"
  journalctl -u vpscontrol -n 20 --no-pager
  exit 1
fi

# 2. API répond en local
if curl -fsSL --max-time 5 http://127.0.0.1:8090/api/setup/status >/dev/null 2>&1; then
  ok "API répond sur http://127.0.0.1:8090"
else
  warn "L'API ne répond pas encore — attends quelques secondes :"
  warn "  journalctl -u vpscontrol -n 30"
fi

# 3. Nginx répond
if [ -n "$DOMAIN" ]; then
  if curl -fsSL --max-time 5 -k "https://127.0.0.1/" -H "Host: ${DOMAIN}" >/dev/null 2>&1; then
    ok "Nginx répond sur HTTPS (via Host: ${DOMAIN})"
  else
    warn "Nginx ne répond pas encore en local. Vérifie :"
    warn "  sudo nginx -t"
    warn "  sudo systemctl status nginx"
  fi
else
  if curl -fsSL --max-time 5 -k "https://127.0.0.1:${PORT}/" >/dev/null 2>&1; then
    ok "Nginx répond sur HTTPS (port ${PORT})"
  else
    warn "Nginx ne répond pas encore en local. Vérifie :"
    warn "  sudo nginx -t"
    warn "  sudo systemctl status nginx"
  fi
fi

# 4. Vérifier que la limite d'upload Nginx est bien en place
if grep -q "client_max_body_size" /etc/nginx/sites-available/vpscontrol.conf; then
  LIMIT_LINE="$(grep "client_max_body_size" /etc/nginx/sites-available/vpscontrol.conf | head -1 | tr -s ' ')"
  ok "Limite d'upload Nginx :${LIMIT_LINE}"
else
  warn "client_max_body_size introuvable dans la conf Nginx !"
  warn "Les uploads ZIP seront limités à 1 MB (défaut Nginx)."
  warn "Ajoute manuellement dans /etc/nginx/sites-available/vpscontrol.conf :"
  warn "  client_max_body_size 30m;"
  warn "Puis : sudo nginx -t && sudo systemctl reload nginx"
fi

# =====================================================================
# Résumé final
# =====================================================================
printf '\n'
print_mini_banner
printf '\n'
printf '%s╔══════════════════════════════════════════════════════════════╗%s\n' "$GREEN" "$NC"
printf '%s║%s              %sINSTALLATION TERMINÉE AVEC SUCCÈS%s               %s║%s\n' "$GREEN" "$NC" "$BOLD" "$NC" "$GREEN" "$NC"
printf '%s╚══════════════════════════════════════════════════════════════╝%s\n' "$GREEN" "$NC"
printf '\n'

printf '  %sAccès panel%s  : %s%s%s\n' "$BOLD" "$NC" "$CYAN" "$ACCESS_URL" "$NC"

if [ -z "$DOMAIN" ]; then
  printf '  %sAttention%s    : certificat auto-signé → avertissement navigateur\n' "$YELLOW" "$NC"
  printf '                 au premier accès (normal). Pour un vrai certificat,\n'
  printf '                 réinstalle avec un domaine.\n'
fi

printf '\n'
printf '  %sDonnées%s      : /opt/vpscontrol/data\n' "$BOLD" "$NC"
printf '  %sApps%s         : /opt/vpscontrol/apps\n' "$BOLD" "$NC"
printf '  %sBackups%s      : /opt/vpscontrol/backups\n' "$BOLD" "$NC"
printf '  %sBinaire%s      : /usr/local/bin/vpscontrol\n' "$BOLD" "$NC"
printf '  %sSource%s       : %s\n' "$BOLD" "$NC" "$PROJECT_DIR"
printf '\n'
printf '  %sMises à jour%s  : %s\n' "$BOLD" "$NC" "$AUTO_UPDATE_MSG"
if [ "${AUTO_UPDATE,,}" != "y" ] && [ "${AUTO_UPDATE,,}" != "yes" ]; then
  printf '                 sudo bash %s/scripts/update.sh\n' "$PROJECT_DIR"
fi

printf '\n'
printf '  %sLimites upload%s : ZIP 30 MB (Nginx + panel)\n' "$BOLD" "$NC"
printf '\n'
printf '  %sCommandes utiles%s\n' "$BOLD" "$NC"
printf '    systemctl status vpscontrol\n'
printf '    systemctl restart vpscontrol\n'
printf '    journalctl -u vpscontrol -f\n'
printf '    sudo nginx -t && sudo systemctl reload nginx\n'
printf '\n'
printf '  %sPremière visite%s : crée ton compte admin.\n' "$BOLD" "$NC"
printf '\n'
printf '%s────────────────────────────────────────────────────────────────%s\n' "$DIM" "$NC"
printf '\n'