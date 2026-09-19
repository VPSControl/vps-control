#!/usr/bin/env bash
# Installeur en une ligne pour VPS Control.
#
# Ce fichier est celui à héberger tel quel sur install.vpscontrol.lordobitotech.xyz
# (simple fichier statique servi en texte brut — voir website/README-hosting.md).
#
# Usage pour l'utilisateur final :
#   curl -fsSL https://install.vpscontrol.lordobitotech.xyz | sudo bash
#   curl -fsSL https://install.vpscontrol.lordobitotech.xyz | sudo bash -s -- panel.mondomaine.com
#
# Le domaine peut aussi être fourni via variable d'environnement, pratique
# quand on ne peut pas passer d'argument après un pipe :
#   curl -fsSL https://install.vpscontrol.lordobitotech.xyz | sudo VPSCONTROL_DOMAIN=panel.mondomaine.com bash
set -euo pipefail

# --- À adapter une fois le dépôt GitHub réellement créé ---
REPO_URL="${VPSCONTROL_REPO_URL:-https://github.com/lordobitotech/vps-control.git}"
BRANCH="${VPSCONTROL_BRANCH:-main}"
# -----------------------------------------------------------

if [ "$(id -u)" -ne 0 ]; then
  echo "Cette installation doit être lancée en root, par exemple :" >&2
  echo "  curl -fsSL https://install.vpscontrol.lordobitotech.xyz | sudo bash" >&2
  exit 1
fi

DOMAIN="${1:-${VPSCONTROL_DOMAIN:-}}"

echo "=================================================================="
echo " VPS Control — installation"
echo "=================================================================="

if ! command -v git >/dev/null 2>&1; then
  echo "==> Installation de git..."
  apt-get update -y
  apt-get install -y git ca-certificates curl
fi

CLONE_DIR="/opt/vpscontrol-src"
if [ -d "$CLONE_DIR/.git" ]; then
  echo "==> Mise à jour du code source existant dans $CLONE_DIR..."
  git -C "$CLONE_DIR" fetch --depth 1 origin "$BRANCH"
  git -C "$CLONE_DIR" checkout "$BRANCH"
  git -C "$CLONE_DIR" reset --hard "origin/$BRANCH"
else
  echo "==> Récupération du code source dans $CLONE_DIR..."
  rm -rf "$CLONE_DIR"
  git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$CLONE_DIR"
fi

cd "$CLONE_DIR"
if [ -n "$DOMAIN" ]; then
  exec bash scripts/install.sh "$DOMAIN"
else
  exec bash scripts/install.sh
fi
