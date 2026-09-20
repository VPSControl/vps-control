#!/usr/bin/env bash
# Updates VPS Control to the latest version from the repo and restarts the service.
# Called either manually (sudo bash scripts/update.sh), from the panel's
# "Update" button, or automatically by the vpscontrol-update.timer if the
# installer enabled it.
set -euo pipefail

SRC_DIR="${VPSCONTROL_SRC_DIR:-/opt/vpscontrol-src}"
BRANCH="${VPSCONTROL_BRANCH:-main}"

echo "[$(date -Is)] Checking for updates in $SRC_DIR..."
cd "$SRC_DIR"

BEFORE="$(git rev-parse HEAD)"
git fetch --depth 1 origin "$BRANCH"
git reset --hard "origin/$BRANCH"
AFTER="$(git rev-parse HEAD)"

if [ "$BEFORE" = "$AFTER" ]; then
  echo "[$(date -Is)] Already up to date (${BEFORE:0:10}), nothing to do."
  exit 0
fi

echo "[$(date -Is)] New version detected (${BEFORE:0:10} -> ${AFTER:0:10}). Building..."
go build -o /usr/local/bin/vpscontrol .

echo "[$(date -Is)] Restarting the service..."
systemctl restart vpscontrol

echo "[$(date -Is)] Update complete."
