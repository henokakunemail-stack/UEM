#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Agent - Linux POSIX Shell Uninstaller
# ==============================================================================
set -euo pipefail

PURGE_DATA="${1:-false}"
UNINSTALL_TOKEN="${UNINSTALL_TOKEN:-${2:-}}"
INSTALL_BIN="/usr/local/bin/endpoint-agent"
CREDS_DIR="/etc/endpoint-agent"

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This uninstaller requires root privileges. Please run with sudo: sudo $0 [purge_data_true_or_false] [uninstall_token]"
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management Agent - Linux Uninstaller"
echo "=========================================================="

# Dual-verification check: prevent unauthorized removal of enrolled endpoints
if [ -f "$CREDS_DIR/creds.json" ] && [ -f "$INSTALL_BIN" ]; then
    if [ -z "$UNINSTALL_TOKEN" ]; then
        if [ -t 0 ]; then
            read -r -p "Enter Administrator Uninstall Verification Token: " UNINSTALL_TOKEN
        else
            echo "[-] ERROR: Managed device requires an admin-issued uninstall token."
            echo "[-] Pass token via env var UNINSTALL_TOKEN or as 2nd argument: sudo $0 false <token>"
            exit 1
        fi
    fi

    echo "[*] Verifying uninstallation authorization with central server..."
    if ! "$INSTALL_BIN" -creds "$CREDS_DIR/creds.json" -verify-uninstall "$UNINSTALL_TOKEN"; then
        echo "[-] ERROR: Verification failed. Invalid or expired uninstallation token."
        echo "[-] Please contact your administrator to generate a token from the Management Console."
        exit 1
    fi
    echo "[+] Uninstallation authorized."
fi

echo "[*] Stopping and disabling systemd service 'endpoint-agent'..."
if [ -f "$INSTALL_BIN" ]; then
    "$INSTALL_BIN" -service stop >/dev/null 2>&1 || true
    if [ -n "$UNINSTALL_TOKEN" ]; then
        "$INSTALL_BIN" -creds "$CREDS_DIR/creds.json" -service uninstall -uninstall-token "$UNINSTALL_TOKEN" >/dev/null 2>&1 || true
    else
        "$INSTALL_BIN" -service uninstall >/dev/null 2>&1 || true
    fi
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
