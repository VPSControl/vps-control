#!/usr/bin/env bash
#
# VPS CTL — désinstallation
#
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Ce script doit être lancé en root (sudo bash scripts/uninstall.sh)" >&2
  exit 1
fi

G=$'\033[38;5;46m'
R=$'\033[0;31m'
Y=$'\033[0;33m'
C=$'\033[0;36m'
B=$'\033[1m'
D=$'\033[2m'
N=$'\033[0m'
if [ ! -t 1 ]; then G=""; R=""; Y=""; C=""; B=""; D=""; N=""; fi

log()  { printf '%s==>%s %s\n' "$C" "$N" "$*"; }
ok()   { printf '%s✓%s %s\n' "$G" "$N" "$*"; }
warn() { printf '%s!%s %s\n' "$Y" "$N" "$*"; }
err()  { printf '%s✗%s %s\n' "$R" "$N" "$*" >&2; }
step() { printf '\n%s▶ %s%s\n' "$B$C" "$*" "$N"; }

print_mini_banner() {
  printf '%s╭─ VPS CTL%s%s ─╮%s\n' "$G" "$D" "$N" "$N"
}

printf '\n'
printf '%s   ██╗   ██╗██████╗ ███████╗     ██████╗████████╗██╗     %s\n' "$R" "$N"
printf '%s   ██║   ██║██╔══██╗██╔════╝    ██╔════╝╚══██╔══╝██║     %s\n' "$R" "$N"
printf '%s   ██║   ██║██████╔╝███████╗    ██║        ██║   ██║     %s\n' "$R" "$N"
printf '%s   ╚██╗ ██╔╝██╔═══╝ ╚════██║    ██║        ██║   ██║     %s\n' "$R" "$N"
printf '%s    ╚████╔╝ ██║     ███████║    ╚██████╗   ██║   ███████╗%s\n' "$R" "$N"
printf '%s     ╚═══╝  ╚═╝     ╚══════╝     ╚═════╝   ╚═╝   ╚══════╝%s\n' "$R" "$N"
printf '\n%s              DÉSINSTALLATION%s\n' "$B" "$N"
printf '\n'

printf '%sEs-tu sûr de vouloir désinstaller VPS Control ? (y/N) :%s ' "$Y" "$N"
read -r CONFIRM
if [[ ! "${CONFIRM,,}" =~ ^y ]]; then
  echo "Annulé."
  exit 0
fi

step "Arrêt des services"
systemctl stop vpscontrol-update.timer 2>/dev/null || true
systemctl disable vpscontrol-update.timer 2>/dev/null || true
systemctl stop vpscontrol 2>/dev/null || true
systemctl disable vpscontrol 2>/dev/null || true
ok "Services arrêtés"

step "Suppression des fichiers systemd"
rm -f /etc/systemd/system/vpscontrol.service
rm -f /etc/systemd/system/vpscontrol-update.service
rm -f /etc/systemd/system/vpscontrol-update.timer
systemctl daemon-reload
systemctl reset-failed 2>/dev/null || true
ok "Fichiers systemd supprimés"

step "Suppression du binaire"
rm -f /usr/local/bin/vpscontrol
ok "Binaire supprimé"

step "Nettoyage Nginx"
rm -f /etc/nginx/sites-enabled/vpscontrol.conf
rm -f /etc/nginx/sites-available/vpscontrol.conf
if nginx -t >/dev/null 2>&1; then
  systemctl reload nginx
  ok "Nginx rechargé"
else
  warn "Nginx en erreur — vérifie /etc/nginx/"
fi

step "Certificat auto-signé"
if [ -d /etc/vpscontrol ]; then
  rm -rf /etc/vpscontrol
  ok "Supprimé (/etc/vpscontrol)"
else
  ok "Aucun certificat local"
fi

printf '\n%sLes données sont conservées par défaut.%s\n' "$Y" "$N"
printf 'Supprimer aussi les DONNÉES (comptes, apps, backups) ? (y/N) : '
read -r DELDATA
if [[ "${DELDATA,,}" =~ ^y ]]; then
  printf '%sSupprimer aussi les conteneurs Docker du panel ? (y/N) :%s ' "$Y" "$N"
  read -r DELCONT
  if [[ "${DELCONT,,}" =~ ^y ]]; then
    log "Suppression des conteneurs..."
    docker ps -a --filter "name=vpscontrol-" --format "{{.Names}}" | xargs -r docker rm -f 2>/dev/null || true
    log "Suppression des images..."
    docker images --filter "reference=vpscontrol-*" --format "{{.Repository}}:{{.Tag}}" | xargs -r docker rmi 2>/dev/null || true
    ok "Conteneurs et images supprimés"
  fi

  rm -rf /opt/vpscontrol/data
  rm -rf /opt/vpscontrol/apps
  rm -rf /opt/vpscontrol/backups
  rmdir /opt/vpscontrol 2>/dev/null || true
  rm -rf /opt/vpscontrol-src
  ok "Données supprimées"
else
  ok "Données conservées dans /opt/vpscontrol"
fi

printf '\n'
print_mini_banner
printf '\n'
printf '%s✓ Désinstallation terminée.%s\n\n' "$G" "$N"