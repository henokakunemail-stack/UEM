#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Server - Linux POSIX Shell Uninstaller
# ==============================================================================
set -euo pipefail

PURGE_DB="${1:-false}"
PURGE_CONFIG="${2:-true}"
INSTALL_BIN="/usr/local/bin/endpoint-server"
DATA_DIR="/var/lib/endpoint-mgmt-server"
CONF_DIR="/etc/endpoint-mgmt-server"

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This uninstaller requires root privileges. Please run with sudo: sudo $0 [purge_db_true_or_false]"
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management Server - Linux Uninstaller"
echo "=========================================================="

echo "[*] Stopping and disabling systemd service 'endpoint-mgmt-server'..."
systemctl stop endpoint-mgmt-server >/dev/null 2>&1 || true
systemctl disable endpoint-mgmt-server >/dev/null 2>&1 || true
rm -f /etc/systemd/system/endpoint-mgmt-server.service
systemctl daemon-reload >/dev/null 2>&1 || true

echo "[*] Removing server binary from $INSTALL_BIN..."
rm -f "$INSTALL_BIN"

if [ "$PURGE_CONFIG" = "true" ] || [ "$PURGE_CONFIG" = "1" ]; then
    echo "[*] Removing configuration and logs from $CONF_DIR..."
    rm -rf "$CONF_DIR"
fi

if [ "$PURGE_DB" = "true" ] || [ "$PURGE_DB" = "1" ] || [ "$PURGE_DB" = "--purge-db" ]; then
    echo "[*] Purging database and backups from $DATA_DIR..."
    rm -rf "$DATA_DIR"
    echo "[+] Database purged."
else
    echo "[*] Retaining database in $DATA_DIR (pass '--purge-db' to remove)."
fi

echo "=========================================================="
echo "[+] Server Uninstallation Complete!"
echo "=========================================================="
