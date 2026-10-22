#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Agent - Linux POSIX Shell Installer
# ==============================================================================
set -euo pipefail

SERVER_URL="${1:-http://localhost:8443}"
ENROLL_TOKEN="${2:-}"
SITE="${3:-}"
INSTALL_BIN="/usr/local/bin/endpoint-agent"
CREDS_PATH="/etc/endpoint-agent/creds.json"

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This installer requires root privileges. Please run with sudo: sudo $0 [server_url] [enroll_token] [site]"
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management Agent - Linux Installer"
echo "=========================================================="

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
case "$(uname -m)" in
    aarch64|arm64) ARCH=arm64 ;;
    *) ARCH=amd64 ;;
esac
BIN_SRC=""
for cand in "$SCRIPT_DIR/endpoint-agent" "$SCRIPT_DIR/agent-linux-$ARCH" "$SCRIPT_DIR/../../agent-linux-$ARCH" "/tmp/agent-linux-$ARCH"; do
    if [ -f "$cand" ]; then BIN_SRC="$cand"; break; fi
done

if [ -z "$BIN_SRC" ]; then
    echo "[-] Agent binary not found. Place 'agent-linux-$ARCH' or 'endpoint-agent' alongside this script."
    exit 1
fi

echo "[+] Using binary: $BIN_SRC"
BIN_CHECK="$BIN_SRC"
# These scripts install whatever agent binary is lying next to them. One built
# before -site and the stored-identity check existed exits at once on -site
# ("flag provided but not defined") or re-enrolls on every run, spending a use
# of a multi-device token each time. Refuse it with a clear reason.
if ! "$BIN_CHECK" -h 2>&1 | grep -q -- '-reenroll'; then
    MSG_OLD="The agent binary at $BIN_CHECK is older than this installer (no -reenroll/-site support). Rebuild it from source or download the current release."
    echo "[-] $MSG_OLD"
    exit 1
fi

mkdir -p /usr/local/bin /etc/endpoint-agent
chmod 700 /etc/endpoint-agent
cp "$BIN_SRC" "$INSTALL_BIN"
chmod 0755 "$INSTALL_BIN"

if [ -n "$ENROLL_TOKEN" ]; then
    echo "[*] Enrolling device with $SERVER_URL..."
    if [ -n "$SITE" ]; then
        "$INSTALL_BIN" -server "$SERVER_URL" -enroll "$ENROLL_TOKEN" -site "$SITE" -enroll-only -creds "$CREDS_PATH" ||
            { echo "[-] Enrollment failed; the agent printed the reason above."; exit 1; }
    else
        "$INSTALL_BIN" -server "$SERVER_URL" -enroll "$ENROLL_TOKEN" -enroll-only -creds "$CREDS_PATH" ||
            { echo "[-] Enrollment failed; the agent printed the reason above."; exit 1; }
    fi
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
