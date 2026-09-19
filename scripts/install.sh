#!/usr/bin/env bash
# Installation de VPS Control sur un VPS Debian/Ubuntu.
#
# Usage :
#   sudo bash scripts/install.sh                          # installation sans domaine (accès via tunnel SSH ou reverse proxy manuel)
#   sudo bash scripts/install.sh panel.mondomaine.com      # installation + HTTPS automatique sur ce domaine (via Caddy)
#   sudo VPSCONTROL_DOMAIN=panel.mondomaine.com bash scripts/install.sh   # équivalent, utile via curl | bash
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Ce script doit être exécuté en root (sudo bash scripts/install.sh)" >&2
  exit 1
fi

DOMAIN="${1:-${VPSCONTROL_DOMAIN:-}}"

# Si aucun domaine n'est passé en argument/variable et qu'un terminal interactif
# est disponible (cas d'une exécution locale, pas via curl | bash sans -s),
# on le demande. Sinon on continue sans domaine.
if [ -z "$DOMAIN" ] && [ -t 0 ]; then
  read -r -p "Nom de domaine à utiliser pour le panel (laisser vide pour passer cette étape) : " DOMAIN || true
fi
if [ -z "$DOMAIN" ] && [ -r /dev/tty ]; then
  read -r -p "Nom de domaine à utiliser pour le panel (laisser vide pour passer cette étape) : " DOMAIN </dev/tty || true
fi

echo "==> Installation des paquets système (docker, git, curl)..."
apt-get update -y
apt-get install -y ca-certificates curl git gnupg

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

# ---------------------------------------------------------------------
# Configuration du domaine (reverse proxy HTTPS automatique via Caddy)
# ---------------------------------------------------------------------
if [ -n "$DOMAIN" ]; then
  echo "==> Configuration du domaine $DOMAIN (Caddy, HTTPS automatique)..."

  if ! command -v caddy >/dev/null 2>&1; then
    apt-get install -y debian-keyring debian-archive-keyring apt-transport-https
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
      | gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
    curl -fsSL 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
      > /etc/apt/sources.list.d/caddy-stable.list
    apt-get update -y
    apt-get install -y caddy
  fi

  CADDY_BLOCK="/etc/caddy/vpscontrol.caddy"
  cat > "$CADDY_BLOCK" <<EOF
${DOMAIN} {
    reverse_proxy 127.0.0.1:8090
}
EOF
  # On importe ce bloc depuis le Caddyfile principal sans écraser une config existante.
  if ! grep -q "vpscontrol.caddy" /etc/caddy/Caddyfile 2>/dev/null; then
    echo "import $CADDY_BLOCK" >> /etc/caddy/Caddyfile
  fi
  systemctl enable --now caddy
  systemctl reload caddy

  if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
    ufw allow 80/tcp || true
    ufw allow 443/tcp || true
  fi

  echo ""
  echo "=================================================================="
  echo " VPS Control est installé et démarré."
  echo " Pensez à pointer un enregistrement DNS A de ${DOMAIN} vers l'IP de ce VPS"
  echo " (si ce n'est pas déjà fait) — Caddy obtiendra le certificat HTTPS"
  echo " automatiquement dès que le DNS pointe correctement."
  echo ""
  echo " Panel accessible sur : https://${DOMAIN}"
  echo "=================================================================="
else
  echo ""
  echo "=================================================================="
  echo " VPS Control est installé et démarré."
  echo " Il écoute en local sur 127.0.0.1:8090 (pas exposé directement à internet)."
  echo ""
  echo " Pour configurer un domaine avec HTTPS automatique plus tard, relancez :"
  echo "   sudo bash scripts/install.sh panel.mondomaine.com"
  echo ""
  echo " En attendant, pour tester rapidement via un tunnel SSH depuis votre machine :"
  echo "   ssh -L 8090:localhost:8090 root@VOTRE_IP"
  echo " puis ouvrez http://localhost:8090 dans votre navigateur."
  echo "=================================================================="
fi
