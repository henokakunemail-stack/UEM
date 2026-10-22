#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Agent - macOS Uninstaller
# ==============================================================================
# Usage: sudo ./uninstall-agent.sh <uninstall_token>
#
# An enrolled agent is removed only with an uninstall token issued from the
# console, the same rule the Windows and Linux uninstallers enforce.
set -euo pipefail

TOKEN="${1:-}"
LABEL="com.endpoint-mgmt.agent"
BIN="/usr/local/bin/endpoint-agent"
CONFIG_DIR="/etc/endpoint-agent"
CREDS_PATH="$CONFIG_DIR/creds.json"

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This uninstaller requires root privileges. Run: sudo $0 <uninstall_token>" >&2
    exit 1
fi

if [ -f "$CREDS_PATH" ] && [ -x "$BIN" ]; then
    if [ -z "$TOKEN" ]; then
        echo "[-] This device is enrolled. Generate an uninstall token in the console and run: sudo $0 <token>" >&2
        exit 1
    fi
    if ! "$BIN" -creds "$CREDS_PATH" -verify-uninstall "$TOKEN"; then
        echo "[-] The uninstall token was rejected by the server." >&2
        exit 1
    fi
fi

launchctl bootout "system/$LABEL" 2>/dev/null || launchctl unload -w "/Library/LaunchDaemons/$LABEL.plist" 2>/dev/null || true
rm -f "/Library/LaunchDaemons/$LABEL.plist"
# The legacy -service install label, in case this device was set up that way.
launchctl bootout system/com.endpoint-mgmt.endpoint-agent 2>/dev/null || true
rm -f /Library/LaunchDaemons/com.endpoint-mgmt.endpoint-agent.plist
rm -f "$BIN" "$BIN.old"
rm -rf "$CONFIG_DIR"
rm -f /var/log/endpoint-agent.log /var/log/endpoint-agent.err
pkgutil --forget com.endpoint-mgmt.agent >/dev/null 2>&1 || true
echo "[+] Endpoint Management Agent removed."
