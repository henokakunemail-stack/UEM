#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Agent - Linux POSIX Shell Uninstaller
# ==============================================================================
set -euo pipefail

PURGE_DATA="${1:-false}"
INSTALL_BIN="/usr/local/bin/endpoint-agent"
CREDS_DIR="/etc/endpoint-agent"

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This uninstaller requires root privileges. Please run with sudo: sudo $0 [purge_data_true_or_false]"
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management Agent - Linux Uninstaller"
echo "=========================================================="

echo "[*] Stopping and disabling systemd service 'endpoint-agent'..."
if [ -f "$INSTALL_BIN" ]; then
    "$INSTALL_BIN" -service stop >/dev/null 2>&1 || true
    "$INSTALL_BIN" -service uninstall >/dev/null 2>&1 || true
fi

systemctl stop endpoint-agent >/dev/null 2>&1 || true
systemctl disable endpoint-agent >/dev/null 2>&1 || true
rm -f /etc/systemd/system/endpoint-agent.service
systemctl daemon-reload >/dev/null 2>&1 || true

echo "[*] Removing binary from $INSTALL_BIN..."
rm -f "$INSTALL_BIN"

if [ "$PURGE_DATA" = "true" ] || [ "$PURGE_DATA" = "1" ] || [ "$PURGE_DATA" = "--purge" ]; then
    echo "[*] Purging credentials from $CREDS_DIR..."
    rm -rf "$CREDS_DIR"
    echo "[+] Data purged."
else
    echo "[*] Retaining credentials in $CREDS_DIR (pass '--purge' to remove)."
fi

echo "=========================================================="
echo "[+] Agent Uninstallation Complete!"
echo "=========================================================="
