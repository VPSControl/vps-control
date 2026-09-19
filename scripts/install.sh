#!/usr/bin/env bash
# Installation de VPS Control sur un VPS Debian/Ubuntu.
# À exécuter en root, depuis la racine du projet (là où se trouve go.mod) :
#   sudo bash scripts/install.sh
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Ce script doit être exécuté en root (sudo bash scripts/install.sh)" >&2
  exit 1
fi

echo "==> Installation des paquets système (docker, git, curl)..."
apt-get update -y
apt-get install -y ca-certificates curl git

if ! command -v docker >/dev/null 2>&1; then
  echo "==> Installation de Docker..."
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker

if ! command -v go >/dev/null 2>&1; then
  echo "==> Installation de Go..."
  GO_VERSION="1.22.5"
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64) GOARCH="amd64" ;;
    aarch64) GOARCH="arm64" ;;
    *) echo "Architecture non supportée automatiquement: $ARCH. Installez Go manuellement." >&2; exit 1 ;;
  esac
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi

echo "==> Compilation de VPS Control..."
PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$PROJECT_DIR"
go mod tidy
go build -o /usr/local/bin/vpscontrol .

echo "==> Préparation des dossiers..."
mkdir -p /opt/vpscontrol/data /opt/vpscontrol/apps

echo "==> Installation du service systemd..."
cp "$PROJECT_DIR/scripts/vpscontrol.service" /etc/systemd/system/vpscontrol.service
systemctl daemon-reload
systemctl enable --now vpscontrol

echo ""
echo "=================================================================="
echo " VPS Control est installé et démarré."
echo " Il écoute en local sur 127.0.0.1:8090 (pas exposé directement à internet)."
echo ""
echo " Pour y accéder depuis l'extérieur en HTTPS, mettez un reverse proxy"
echo " devant (Caddy ou Nginx + certbot) — voir le README.md."
echo ""
echo " En attendant, pour tester rapidement via un tunnel SSH depuis votre machine :"
echo "   ssh -L 8090:localhost:8090 root@VOTRE_IP"
echo " puis ouvrez http://localhost:8090 dans votre navigateur."
echo "=================================================================="
