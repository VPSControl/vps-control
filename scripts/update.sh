#!/usr/bin/env bash
# Met à jour VPS Control vers la dernière version du dépôt et redémarre le service.
# Appelé soit manuellement (sudo bash scripts/update.sh), soit depuis le panel
# (bouton "Mettre à jour" du panel), soit automatiquement par le minuteur
# vpscontrol-update.timer si l'installeur l'a activé.
set -euo pipefail

SRC_DIR="${VPSCONTROL_SRC_DIR:-/opt/vpscontrol-src}"
BRANCH="${VPSCONTROL_BRANCH:-main}"

echo "[$(date -Is)] Vérification des mises à jour dans $SRC_DIR..."
cd "$SRC_DIR"

BEFORE="$(git rev-parse HEAD)"
git fetch --depth 1 origin "$BRANCH"
git reset --hard "origin/$BRANCH"
AFTER="$(git rev-parse HEAD)"

if [ "$BEFORE" = "$AFTER" ]; then
  echo "[$(date -Is)] Déjà à jour (${BEFORE:0:10}), rien à faire."
  exit 0
fi

echo "[$(date -Is)] Nouvelle version détectée (${BEFORE:0:10} -> ${AFTER:0:10}). Compilation..."
go build -o /usr/local/bin/vpscontrol .

echo "[$(date -Is)] Redémarrage du service..."
systemctl restart vpscontrol

echo "[$(date -Is)] Mise à jour terminée."
