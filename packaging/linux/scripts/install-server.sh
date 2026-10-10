#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Server - Linux POSIX Shell Installer
# ==============================================================================
set -euo pipefail

LISTEN_ADDR="${1:-:8443}"
ADMIN_PASSWORD="${2:-}"
INSTALL_BIN="/usr/local/bin/endpoint-server"
DATA_DIR="/var/lib/endpoint-mgmt-server"
CONF_DIR="/etc/endpoint-mgmt-server"
ENV_FILE="$CONF_DIR/server.env"

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This installer requires root privileges. Please run with sudo: sudo $0 [listen_addr] [admin_password]"
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management Server - Linux Installer"
echo "=========================================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC=""
for cand in "$SCRIPT_DIR/endpoint-server" "$SCRIPT_DIR/server-linux-amd64" "$SCRIPT_DIR/../../server-linux-amd64" /tmp/server-linux-amd64; do
    if [ -f "$cand" ]; then BIN_SRC="$cand"; break; fi
done

if [ -z "$BIN_SRC" ]; then
    echo "[-] Server binary not found. Place 'server-linux-amd64' or 'endpoint-server' alongside this script."
    exit 1
fi

echo "[+] Using binary: $BIN_SRC"

mkdir -p /usr/local/bin "$DATA_DIR/backups" "$CONF_DIR"
cp "$BIN_SRC" "$INSTALL_BIN"
chmod 0755 "$INSTALL_BIN"

echo "[*] Generating $ENV_FILE..."
cat <<EOF > "$ENV_FILE"
# Runtime configuration for endpoint-mgmt-server
HTTP_ADDR=$LISTEN_ADDR
DB_PATH=$DATA_DIR/server.db
BACKUP_DIR=$DATA_DIR/backups
LOG_FILE=$DATA_DIR/server.log
LOG_LEVEL=info
ALLOWED_ORIGIN_DOMAINS=
JWT_SECRET=
EOF

if [ -n "$ADMIN_PASSWORD" ]; then
    echo "ADMIN_PASSWORD=$ADMIN_PASSWORD" >> "$ENV_FILE"
fi
chmod 0600 "$ENV_FILE"

echo "[*] Creating systemd service..."
cat <<EOF > /etc/systemd/system/endpoint-mgmt-server.service
[Unit]
Description=Enterprise Endpoint Management Server
After=network.target

[Service]
Type=simple
ExecStart=$INSTALL_BIN -env-file $ENV_FILE
Restart=always
RestartSec=5s
LimitNOFILE=65535
WorkingDirectory=$DATA_DIR

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable endpoint-mgmt-server
systemctl restart endpoint-mgmt-server
sleep 2

if systemctl is-active --quiet endpoint-mgmt-server; then
    echo "[+] Server is ACTIVE and RUNNING on $LISTEN_ADDR."
else
    echo "[-] Status: $(systemctl is-active endpoint-mgmt-server || true)"
fi

echo "=========================================================="
echo "[+] Server Installation Complete!"
echo "=========================================================="
