#!/usr/bin/env bash
#
# VPS CTL — update script
# Récupère la dernière version depuis GitHub, rebuild, restart.
#
# Appelé soit :
#   - manuellement : sudo bash scripts/update.sh
#   - par le panel : bouton "Update now" dans l'onglet System
#   - par le webhook GitHub sur push
#   - par le timer systemd quotidien (si activé)
#
set -euo pipefail

# =====================================================================
# Couleurs
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

log()  { printf '%s[%s]%s ==> %s\n' "$BLUE" "$(date -Is)" "$NC" "$*"; }
ok()   { printf '%s[%s]%s ✓ %s\n' "$GREEN" "$(date -Is)" "$NC" "$*"; }
warn() { printf '%s[%s]%s ! %s\n' "$YELLOW" "$(date -Is)" "$NC" "$*"; }
err()  { printf '%s[%s]%s ✗ %s\n' "$RED" "$(date -Is)" "$NC" "$*" >&2; }

print_mini_banner() {
  local G=$'\033[38;5;46m'
  local D=$'\033[38;5;28m'
  local N=$'\033[0m'
  if [ ! -t 1 ]; then G=""; D=""; N=""; fi
  printf '%s╭─ VPS CTL%s%s ─╮%s\n' "$G" "$D" "$N" "$NC"
}

# =====================================================================
# Configuration
# =====================================================================
SRC_DIR="${VPSCONTROL_SRC_DIR:-/opt/vpscontrol-src}"
BRANCH="${VPSCONTROL_BRANCH:-main}"
BINARY="/usr/local/bin/vpscontrol"
SERVICE="vpscontrol"
LOGFILE="/var/log/vpscontrol-update.log"

# =====================================================================
# Vérifications
# =====================================================================
print_mini_banner
printf '\n'

if [ "$(id -u)" -ne 0 ]; then
  err "Ce script doit être lancé en root (sudo bash scripts/update.sh)"
  exit 1
fi

if [ ! -d "$SRC_DIR" ]; then
  err "Dossier source introuvable : $SRC_DIR"
  err "Le panel a-t-il été installé via git ?"
  exit 1
fi

if [ ! -d "$SRC_DIR/.git" ]; then
  err "$SRC_DIR n'est pas un dépôt git."
  err "Clone-le d'abord :"
  err "  sudo git clone <repo> $SRC_DIR"
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  err "Go n'est pas installé. Installe-le puis relance."
  exit 1
fi

cd "$SRC_DIR"

# Fix git safe.directory (root dans un dossier utilisateur)
git config --global --add safe.directory "$SRC_DIR" 2>/dev/null || true

# =====================================================================
# Vérification du remote
# =====================================================================
REMOTE_URL="$(git remote get-url origin 2>/dev/null || echo '')"
if [ -z "$REMOTE_URL" ]; then
  err "Aucun remote 'origin' configuré dans $SRC_DIR"
  err "Ajoute-le : sudo git remote add origin <repo-url>"
  exit 1
fi

ok "Remote : $REMOTE_URL"
ok "Branche : $BRANCH"

# =====================================================================
# Fetch
# =====================================================================
log "Vérification des mises à jour..."

BEFORE="$(git rev-parse HEAD)"

# Récupère la dernière version
if ! git fetch --depth 1 origin "$BRANCH" 2>&1; then
  err "Échec de 'git fetch'. Vérifie ta connexion réseau et l'accès au repo."
  exit 1
fi

REMOTE_HEAD="$(git rev-parse "origin/$BRANCH" 2>/dev/null || echo '')"
if [ -z "$REMOTE_HEAD" ]; then
  err "Impossible de lire origin/$BRANCH"
  exit 1
fi

if [ "$BEFORE" = "$REMOTE_HEAD" ]; then
  ok "Déjà à jour (${BEFORE:0:10}). Rien à faire."
  exit 0
fi

log "Nouvelle version détectée : ${BEFORE:0:10} → ${REMOTE_HEAD:0:10}"

# =====================================================================
# Reset hard sur origin
# =====================================================================
log "Mise à jour du code source..."

if ! git reset --hard "origin/$BRANCH" 2>&1; then
  err "Échec de 'git reset --hard origin/$BRANCH'"
  exit 1
fi

AFTER="$(git rev-parse HEAD)"
ok "Code mis à jour : ${AFTER:0:10}"

# =====================================================================
# Build
# =====================================================================
log "Résolution des dépendances (go mod tidy)..."
go mod tidy >/dev/null 2>&1 || true

log "Compilation du binaire..."
if ! go build -o "$BINARY" . ; then
  err "Échec de la compilation. Le binaire n'a PAS été remplacé."
  err "Rollback au commit précédent..."
  git reset --hard "$BEFORE" 2>/dev/null || true
  exit 1
fi

chmod +x "$BINARY"
SIZE="$(du -h "$BINARY" | awk '{print $1}')"
ok "Binaire compilé : $BINARY ($SIZE)"

# =====================================================================
# Restart service
# =====================================================================
log "Redémarrage du service $SERVICE..."

if systemctl is-active --quiet "$SERVICE"; then
  if ! systemctl restart "$SERVICE"; then
    err "Échec du redémarrage du service."
    exit 1
  fi
  sleep 2
  if systemctl is-active --quiet "$SERVICE"; then
    ok "Service $SERVICE redémarré avec succès."
  else
    err "Le service n'est pas actif après redémarrage. Logs :"
    journalctl -u "$SERVICE" -n 20 --no-pager
    exit 1
  fi
else
  warn "Service $SERVICE n'était pas actif. Démarrage..."
  systemctl start "$SERVICE"
  ok "Service $SERVICE démarré."
fi

# =====================================================================
# Vérification API
# =====================================================================
sleep 1
if curl -fsSL --max-time 5 http://127.0.0.1:8090/api/setup/status >/dev/null 2>&1; then
  ok "API répond sur http://127.0.0.1:8090"
else
  warn "L'API ne répond pas encore. Vérifie :"
  warn "  journalctl -u $SERVICE -n 30"
fi

# =====================================================================
# Résumé
# =====================================================================
printf '\n'
print_mini_banner
printf '\n'
printf '%s✓ Mise à jour terminée%s\n' "$GREEN" "$NC"
printf '  %sVersion%s : %s → %s\n' "$BOLD" "$NC" "${BEFORE:0:10}" "${AFTER:0:10}"
printf '  %sBinaire%s : %s\n' "$BOLD" "$NC" "$BINARY"
printf '  %sService%s : %s\n' "$BOLD" "$NC" "$SERVICE"
printf '\n'

exit 0