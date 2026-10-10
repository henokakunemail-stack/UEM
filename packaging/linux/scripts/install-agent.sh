#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Agent - Linux POSIX Shell Installer
# ==============================================================================
set -euo pipefail

SERVER_URL="${1:-http://localhost:8443}"
ENROLL_TOKEN="${2:-}"
INSTALL_BIN="/usr/local/bin/endpoint-agent"
CREDS_PATH="/etc/endpoint-agent/creds.json"

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This installer requires root privileges. Please run with sudo: sudo $0 [server_url] [enroll_token]"
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management Agent - Linux Installer"
echo "=========================================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC=""
for cand in "$SCRIPT_DIR/endpoint-agent" "$SCRIPT_DIR/agent-linux-amd64" "$SCRIPT_DIR/../../agent-linux-amd64" /tmp/agent-linux-amd64; do
    if [ -f "$cand" ]; then BIN_SRC="$cand"; break; fi
done

if [ -z "$BIN_SRC" ]; then
    echo "[-] Agent binary not found. Place 'agent-linux-amd64' or 'endpoint-agent' alongside this script."
    exit 1
fi

echo "[+] Using binary: $BIN_SRC"

mkdir -p /usr/local/bin /etc/endpoint-agent
chmod 700 /etc/endpoint-agent
cp "$BIN_SRC" "$INSTALL_BIN"
chmod 0755 "$INSTALL_BIN"

if [ -n "$ENROLL_TOKEN" ]; then
    echo "[*] Enrolling device with $SERVER_URL..."
    "$INSTALL_BIN" -server "$SERVER_URL" -enroll "$ENROLL_TOKEN" -creds "$CREDS_PATH" || echo "[!] Enrollment warning (will continue)"
fi

echo "[*] Registering and starting systemd service 'endpoint-agent'..."
"$INSTALL_BIN" -server "$SERVER_URL" -creds "$CREDS_PATH" -service install || true
"$INSTALL_BIN" -service start || systemctl start endpoint-agent || true

sleep 1
if systemctl is-active --quiet endpoint-agent; then
    echo "[+] Endpoint Agent is ACTIVE and RUNNING as root daemon."
else
    echo "[-] Service status: $(systemctl is-active endpoint-agent || true)"
fi

echo "=========================================================="
echo "[+] Agent Installation Complete!"
echo "=========================================================="
