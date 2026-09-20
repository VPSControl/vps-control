#!/usr/bin/env bash
# Installs VPS Control on a Debian/Ubuntu VPS.
#
# Usage:
#   sudo bash scripts/install.sh
#
# The script asks two questions (with sensible defaults if you just hit
# Enter): which domain name to use (leave empty to access via the VPS's IP)
# and whether you want a daily safety-net update check enabled on top of
# the GitHub webhook (see the end of this script) that applies pushes
# instantly.
#
# For non-interactive use (e.g. via curl | bash), you can also supply
# everything through environment variables:
#   VPSCONTROL_DOMAIN=panel.example.com sudo -E bash scripts/install.sh
#   VPSCONTROL_AUTO_UPDATE=yes sudo -E bash scripts/install.sh
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "This script must be run as root (sudo bash scripts/install.sh)" >&2
  exit 1
fi

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ---------------------------------------------------------------------
# Small prompt helper that works even when the script is run via
# curl | bash (stdin already consumed by the pipe): it reads from
# /dev/tty when possible, and falls back to the default otherwise.
# ---------------------------------------------------------------------
ask() {
  local prompt="$1" default="$2" var
  if [ -r /dev/tty ]; then
    read -r -p "$prompt" var </dev/tty || true
  fi
  echo "${var:-$default}"
}

echo "=================================================================="
echo " VPS Control — installation"
echo "=================================================================="
echo ""

DOMAIN="${VPSCONTROL_DOMAIN:-}"
if [ -z "$DOMAIN" ]; then
  DOMAIN="$(ask "Domain name already pointing at this VPS (leave empty to use the server's IP directly): " "")"
fi

AUTO_UPDATE="${VPSCONTROL_AUTO_UPDATE:-}"
if [ -z "$AUTO_UPDATE" ]; then
  AUTO_UPDATE="$(ask "Also enable a daily safety-net update check, in case a GitHub webhook push is ever missed? (y/N): " "n")"
fi

echo ""
echo "==> Installing system packages (docker, git, nginx, curl)..."
apt-get update -y
apt-get install -y ca-certificates curl git gnupg nginx openssl

if ! command -v docker >/dev/null 2>&1; then
  echo "==> Installing Docker..."
  curl -fsSL https://get.docker.com | sh
fi
systemctl enable --now docker

if ! command -v go >/dev/null 2>&1; then
  echo "==> Installing Go..."
  GO_VERSION="1.22.5"
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64) GOARCH="amd64" ;;
    aarch64) GOARCH="arm64" ;;
    *) echo "Architecture not auto-supported: $ARCH. Please install Go manually." >&2; exit 1 ;;
  esac
  curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GOARCH}.tar.gz" -o /tmp/go.tar.gz
  rm -rf /usr/local/go
  tar -C /usr/local -xzf /tmp/go.tar.gz
  ln -sf /usr/local/go/bin/go /usr/local/bin/go
  ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt
fi

echo "==> Building VPS Control..."
cd "$PROJECT_DIR"
go mod tidy
go build -o /usr/local/bin/vpscontrol .

echo "==> Preparing directories..."
mkdir -p /opt/vpscontrol/data /opt/vpscontrol/apps

echo "==> Installing the systemd service..."
sed "s|/opt/vpscontrol-src|${PROJECT_DIR}|g" "$PROJECT_DIR/scripts/vpscontrol.service" > /etc/systemd/system/vpscontrol.service
systemctl daemon-reload
systemctl enable --now vpscontrol

# ---------------------------------------------------------------------
# Nginx reverse proxy + HTTPS access
# ---------------------------------------------------------------------
rm -f /etc/nginx/sites-enabled/default

ACCESS_URL=""

if [ -n "$DOMAIN" ]; then
  echo "==> Configuring Nginx for ${DOMAIN}..."
  cat > /etc/nginx/sites-available/vpscontrol.conf <<NGINX_DOMAIN_CONF
server {
    listen 80;
    server_name ${DOMAIN};

    location / {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
}
NGINX_DOMAIN_CONF
  ln -sf /etc/nginx/sites-available/vpscontrol.conf /etc/nginx/sites-enabled/vpscontrol.conf
  nginx -t && systemctl reload nginx

  if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
    ufw allow 80/tcp || true
    ufw allow 443/tcp || true
  fi

  echo "==> Obtaining an HTTPS certificate via Certbot for ${DOMAIN}..."
  apt-get install -y certbot python3-certbot-nginx
  if [ -r /dev/tty ]; then
    # Deliberately interactive: Certbot will ask for the email address and
    # terms-of-service acceptance itself, exactly as it usually does.
    certbot --nginx -d "$DOMAIN" </dev/tty || {
      echo "!! Certbot could not finish automatically." >&2
      echo "   You can rerun it manually later with:" >&2
      echo "     sudo certbot --nginx -d ${DOMAIN}" >&2
    }
  else
    echo "!! No interactive terminal available for Certbot (non-interactive install)." >&2
    echo "   The panel is reachable over HTTP for now. Run this manually afterwards:" >&2
    echo "     sudo certbot --nginx -d ${DOMAIN}" >&2
  fi
  ACCESS_URL="https://${DOMAIN}"
else
  PORT="$(ask "Port to use for HTTPS access via the IP (default 8443): " "8443")"
  PUBLIC_IP="$(curl -4 -fsSL --max-time 5 ifconfig.me || hostname -I | awk '{print $1}')"

  echo "==> Generating a self-signed certificate (direct IP access)..."
  mkdir -p /etc/vpscontrol/ssl
  openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
    -keyout /etc/vpscontrol/ssl/selfsigned.key \
    -out /etc/vpscontrol/ssl/selfsigned.crt \
    -subj "/CN=${PUBLIC_IP}" >/dev/null 2>&1

  echo "==> Configuring Nginx on port ${PORT}..."
  cat > /etc/nginx/sites-available/vpscontrol.conf <<NGINX_IP_CONF
server {
    listen ${PORT} ssl;
    server_name _;

    ssl_certificate     /etc/vpscontrol/ssl/selfsigned.crt;
    ssl_certificate_key /etc/vpscontrol/ssl/selfsigned.key;

    location / {
        proxy_pass http://127.0.0.1:8090;
        proxy_set_header Host \$host;
        proxy_set_header X-Real-IP \$remote_addr;
        proxy_set_header X-Forwarded-For \$proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto \$scheme;
    }
}
NGINX_IP_CONF
  ln -sf /etc/nginx/sites-available/vpscontrol.conf /etc/nginx/sites-enabled/vpscontrol.conf
  nginx -t && systemctl reload nginx

  if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
    ufw allow "${PORT}/tcp" || true
  fi

  ACCESS_URL="https://${PUBLIC_IP}:${PORT}"
fi

# ---------------------------------------------------------------------
# Automatic updates (optional)
# ---------------------------------------------------------------------
case "${AUTO_UPDATE,,}" in
  y|yes)
    echo "==> Enabling the daily safety-net update check..."
    sed "s|/opt/vpscontrol-src|${PROJECT_DIR}|g" "$PROJECT_DIR/scripts/vpscontrol-update.service" > /etc/systemd/system/vpscontrol-update.service
    cp "$PROJECT_DIR/scripts/vpscontrol-update.timer" /etc/systemd/system/vpscontrol-update.timer
    systemctl daemon-reload
    systemctl enable --now vpscontrol-update.timer
    AUTO_UPDATE_MSG="Daily safety-net check enabled (staggered randomly by 0-30 min)."
    ;;
  *)
    AUTO_UPDATE_MSG="Daily safety-net check disabled. The GitHub webhook (below) is still the main update path; you can also update anytime with: sudo bash ${PROJECT_DIR}/scripts/update.sh, or from the panel's System tab."
    ;;
esac

# ---------------------------------------------------------------------
# GitHub webhook for instant updates
#
# VPS Control isn't a static site you drop somewhere and forget — it's a
# running service, so "update" means: pull the new code into $PROJECT_DIR,
# rebuild the binary, and restart the systemd unit, exactly like updating
# nginx or any other service. The panel generates a random secret on first
# boot and exposes a signed webhook endpoint so that pushing to GitHub
# applies that update immediately, instead of waiting on a timer.
# ---------------------------------------------------------------------
WEBHOOK_SECRET=""
for i in 1 2 3 4 5; do
  if [ -f /opt/vpscontrol/data/webhook.secret ]; then
    WEBHOOK_SECRET="$(od -An -tx1 /opt/vpscontrol/data/webhook.secret | tr -d ' \n')"
    break
  fi
  sleep 1
done

echo ""
echo "=================================================================="
echo " VPS Control is installed and running."
echo ""
echo " Access: ${ACCESS_URL}"
if [ -z "$DOMAIN" ]; then
  echo " (self-signed certificate: your browser will show a security"
  echo "  warning the first time, that's expected — click \"advanced\""
  echo "  then \"proceed\". For a real certificate, rerun the installer"
  echo "  with a domain name.)"
fi
echo ""
echo " Daily safety-net updates: ${AUTO_UPDATE_MSG}"
echo ""
echo " To make pushes to GitHub apply instantly to this running service,"
echo " add a webhook on your repo (Settings -> Webhooks -> Add webhook):"
echo "   Payload URL:  ${ACCESS_URL}/api/webhook/update"
if [ -n "$WEBHOOK_SECRET" ]; then
  echo "   Secret:       ${WEBHOOK_SECRET}"
else
  echo "   Secret:       (run 'sudo cat /opt/vpscontrol/data/webhook.secret | od -An -tx1 | tr -d \" \\n\"'"
  echo "                  or check the panel's System tab)"
fi
echo "   Content type: application/json"
echo "   Events:       Just the push event"
echo " (Also visible anytime in the panel's System tab.)"
echo "=================================================================="
