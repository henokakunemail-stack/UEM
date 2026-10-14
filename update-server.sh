#!/usr/bin/env bash
set -euo pipefail

# Enterprise Endpoint Manager - Linux Server Update Script
# Usage: sudo ./update-server.sh

INSTALL_DIR="${INSTALL_DIR:-/opt/enterprise-manager}"
SERVICE_NAME="${SERVICE_NAME:-enterprise-manager}"

echo "[*] Starting server update..."

# 1. Pull latest changes (if running inside git repo)
if [ -d ".git" ]; then
    echo "[*] Pulling latest git repository changes..."
    git pull origin main
fi

# 2. Build frontend web console
echo "[*] Building web console..."
cd web-console
npm ci --silent
npm run build
cd ..

# 3. Build Go backend binary
echo "[*] Building Go backend server binary..."
go build -ldflags="-s -w" -o server-bin ./server/cmd/server

# 4. Deploy binaries and assets to install directory
echo "[*] Deploying binaries and static assets..."
sudo mkdir -p "$INSTALL_DIR/dist"
sudo cp server-bin "$INSTALL_DIR/enterprise-manager"
sudo cp -r server/cmd/server/dist/* "$INSTALL_DIR/dist/"
sudo chmod +x "$INSTALL_DIR/enterprise-manager"

# 5. Restart systemd service
if systemctl list-unit-files | grep -q "$SERVICE_NAME.service"; then
    echo "[*] Restarting systemd service ($SERVICE_NAME)..."
    sudo systemctl restart "$SERVICE_NAME"
    sudo systemctl status "$SERVICE_NAME" --no-pager
else
    echo "[!] Systemd service '$SERVICE_NAME' not found. Please restart the server process manually."
fi

echo "[✓] Server update completed successfully."
