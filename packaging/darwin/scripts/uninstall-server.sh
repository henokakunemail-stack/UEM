#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Server - macOS Uninstaller
# ==============================================================================
# Usage: sudo ./uninstall-server.sh [--purge]
#
# Removes the LaunchDaemon and the program. The database and configuration are
# kept unless --purge is given, because they hold every enrolled device.
set -euo pipefail

LABEL="com.endpoint-mgmt.server"
if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This uninstaller requires root privileges. Run: sudo $0 [--purge]" >&2
    exit 1
fi

launchctl bootout "system/$LABEL" 2>/dev/null || launchctl unload -w "/Library/LaunchDaemons/$LABEL.plist" 2>/dev/null || true
rm -f "/Library/LaunchDaemons/$LABEL.plist"
rm -rf /usr/local/endpoint-server
if [ "${1:-}" = "--purge" ]; then
    rm -rf /usr/local/var/endpoint-mgmt-server /usr/local/etc/endpoint-mgmt-server
    echo "[+] Server, database and configuration removed."
else
    echo "[+] Server removed. Database and configuration kept in"
    echo "    /usr/local/var/endpoint-mgmt-server and /usr/local/etc/endpoint-mgmt-server (use --purge to delete)."
fi
