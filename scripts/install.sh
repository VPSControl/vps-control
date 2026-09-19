#!/usr/bin/env bash
# Installation de VPS Control sur un VPS Debian/Ubuntu.
#
# Usage :
#   sudo bash scripts/install.sh
#
# Le script pose deux questions (avec valeurs par défaut si vous appuyez juste
# sur Entrée) : le nom de domaine à utiliser (laisser vide pour accéder via
# l'IP du VPS) et si vous voulez activer les mises à jour automatiques.
#
# Pour une utilisation non interactive (via curl | bash par exemple), vous
# pouvez aussi tout fournir par variables d'environnement :
#   VPSCONTROL_DOMAIN=panel.mondomaine.com sudo -E bash scripts/install.sh
#   VPSCONTROL_AUTO_UPDATE=yes sudo -E bash scripts/install.sh
set -euo pipefail

if [ "$(id -u)" -ne 0 ]; then
  echo "Ce script doit être exécuté en root (sudo bash scripts/install.sh)" >&2
  exit 1
fi

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# ---------------------------------------------------------------------
# Petit helper de prompt qui fonctionne même si le script est lancé via
# curl | bash (stdin déjà consommé par le pipe) : on lit depuis /dev/tty
# quand c'est possible, et on retombe sur la valeur par défaut sinon.
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
  DOMAIN="$(ask "Nom de domaine pointant déjà vers ce VPS (laisser vide pour utiliser directement l'IP du serveur) : " "")"
fi

AUTO_UPDATE="${VPSCONTROL_AUTO_UPDATE:-}"
if [ -z "$AUTO_UPDATE" ]; then
  AUTO_UPDATE="$(ask "Activer les mises à jour automatiques quotidiennes ? (o/N) : " "n")"
fi

echo ""
echo "==> Installation des paquets système (docker, git, nginx, curl)..."
apt-get update -y
apt-get install -y ca-certificates curl git gnupg nginx openssl

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
cd "$PROJECT_DIR"
go mod tidy
go build -o /usr/local/bin/vpscontrol .

echo "==> Préparation des dossiers..."
mkdir -p /opt/vpscontrol/data /opt/vpscontrol/apps

echo "==> Installation du service systemd..."
sed "s|/opt/vpscontrol-src|${PROJECT_DIR}|g" "$PROJECT_DIR/scripts/vpscontrol.service" > /etc/systemd/system/vpscontrol.service
systemctl daemon-reload
systemctl enable --now vpscontrol

# ---------------------------------------------------------------------
# Reverse proxy Nginx + accès HTTPS
# ---------------------------------------------------------------------
rm -f /etc/nginx/sites-enabled/default

ACCESS_URL=""

if [ -n "$DOMAIN" ]; then
  echo "==> Configuration de Nginx pour ${DOMAIN}..."
  cat > /etc/nginx/sites-available/vpscontrol.conf <<EOF
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
EOF
  ln -sf /etc/nginx/sites-available/vpscontrol.conf /etc/nginx/sites-enabled/vpscontrol.conf
  nginx -t && systemctl reload nginx

  if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
    ufw allow 80/tcp || true
    ufw allow 443/tcp || true
  fi

  echo "==> Obtention du certificat HTTPS via Certbot pour ${DOMAIN}..."
  apt-get install -y certbot python3-certbot-nginx
  if [ -r /dev/tty ]; then
    # Interactif volontairement : Certbot va lui-même demander l'email et
    # l'acceptation des conditions d'utilisation, comme il le fait d'habitude.
    certbot --nginx -d "$DOMAIN" </dev/tty || {
      echo "!! Certbot n'a pas pu terminer automatiquement." >&2
      echo "   Vous pouvez relancer manuellement plus tard avec :" >&2
      echo "     sudo certbot --nginx -d ${DOMAIN}" >&2
    }
  else
    echo "!! Pas de terminal interactif disponible pour Certbot (installation non interactive)." >&2
    echo "   Le panel est accessible en HTTP pour l'instant. Lancez ensuite manuellement :" >&2
    echo "     sudo certbot --nginx -d ${DOMAIN}" >&2
  fi
  ACCESS_URL="https://${DOMAIN}"
else
  PORT="$(ask "Port à utiliser pour accéder au panel en HTTPS via l'IP (par défaut 8443) : " "8443")"
  PUBLIC_IP="$(curl -4 -fsSL --max-time 5 ifconfig.me || hostname -I | awk '{print $1}')"

  echo "==> Génération d'un certificat auto-signé (accès direct par IP)..."
  mkdir -p /etc/vpscontrol/ssl
  openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
    -keyout /etc/vpscontrol/ssl/selfsigned.key \
    -out /etc/vpscontrol/ssl/selfsigned.crt \
    -subj "/CN=${PUBLIC_IP}" >/dev/null 2>&1

  echo "==> Configuration de Nginx sur le port ${PORT}..."
  cat > /etc/nginx/sites-available/vpscontrol.conf <<EOF
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
EOF
  ln -sf /etc/nginx/sites-available/vpscontrol.conf /etc/nginx/sites-enabled/vpscontrol.conf
  nginx -t && systemctl reload nginx

  if command -v ufw >/dev/null 2>&1 && ufw status | grep -q "Status: active"; then
    ufw allow "${PORT}/tcp" || true
  fi

  ACCESS_URL="https://${PUBLIC_IP}:${PORT}"
fi

# ---------------------------------------------------------------------
# Mises à jour automatiques (optionnel)
# ---------------------------------------------------------------------
case "${AUTO_UPDATE,,}" in
  o|oui|y|yes)
    echo "==> Activation des mises à jour automatiques quotidiennes..."
    sed "s|/opt/vpscontrol-src|${PROJECT_DIR}|g" "$PROJECT_DIR/scripts/vpscontrol-update.service" > /etc/systemd/system/vpscontrol-update.service
    cp "$PROJECT_DIR/scripts/vpscontrol-update.timer" /etc/systemd/system/vpscontrol-update.timer
    systemctl daemon-reload
    systemctl enable --now vpscontrol-update.timer
    AUTO_UPDATE_MSG="Activées (vérification tous les jours, décalée aléatoirement de 0 à 30 min)."
    ;;
  *)
    AUTO_UPDATE_MSG="Désactivées. Mettez à jour manuellement avec : sudo bash ${PROJECT_DIR}/scripts/update.sh (ou depuis l'onglet Système du panel)."
    ;;
esac

echo ""
echo "=================================================================="
echo " VPS Control est installé et démarré."
echo ""
echo " Accès : ${ACCESS_URL}"
if [ -z "$DOMAIN" ]; then
  echo " (certificat auto-signé : votre navigateur affichera un avertissement"
  echo "  de sécurité la première fois, c'est normal — cliquez sur \"avancé\""
  echo "  puis \"continuer\". Pour un vrai certificat, relancez l'installeur"
  echo "  avec un nom de domaine.)"
fi
echo ""
echo " Mises à jour automatiques : ${AUTO_UPDATE_MSG}"
echo "=================================================================="
