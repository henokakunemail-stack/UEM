#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Agent - macOS Installer (LaunchDaemon)
# ==============================================================================
# Usage: sudo ./install-agent.sh <server_url> <enroll_token> [site]
#
# Installs the agent next to this script as /usr/local/bin/endpoint-agent,
# enrolls it, and registers the com.endpoint-mgmt.agent LaunchDaemon -- the same
# label and layout as EndpointAgent-macOS.pkg, so a device installed either way
# updates and uninstalls the same way.
set -euo pipefail

SERVER_URL="${1:-}"
ENROLL_TOKEN="${2:-}"
SITE="${3:-}"
LABEL="com.endpoint-mgmt.agent"
INSTALL_BIN="/usr/local/bin/endpoint-agent"
CONFIG_DIR="/etc/endpoint-agent"
CREDS_PATH="$CONFIG_DIR/creds.json"
PLIST="/Library/LaunchDaemons/$LABEL.plist"

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This installer requires root privileges. Run: sudo $0 <server_url> <enroll_token> [site]" >&2
    exit 1
fi
if [ "$(uname -s)" != "Darwin" ]; then
    echo "[-] This installer is for macOS. Use packaging/linux/scripts/install-agent.sh on Linux." >&2
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management Agent - macOS Installer"
echo "=========================================================="

# uname -m reports x86_64 inside a Rosetta shell on Apple Silicon, so the CPU
# is asked directly: an Intel agent under Rosetta would run, but slower, and it
# would then be offered Intel updates for the life of the install.
if [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = "1" ]; then
    ARCH=arm64
else
    ARCH=amd64
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC=""
for cand in "$SCRIPT_DIR/endpoint-agent" "$SCRIPT_DIR/agent-darwin-$ARCH" "$(pwd)/agent-darwin-$ARCH"; do
    if [ -f "$cand" ]; then BIN_SRC="$cand"; break; fi
done
if [ -z "$BIN_SRC" ]; then
    echo "[-] Agent binary not found. Place 'agent-darwin-$ARCH' or 'endpoint-agent' alongside this script." >&2
    exit 1
fi
echo "[+] Using binary: $BIN_SRC ($ARCH)"

# Stop a previous install before its binary is replaced.
launchctl bootout "system/$LABEL" 2>/dev/null || launchctl unload -w "$PLIST" 2>/dev/null || true

mkdir -p /usr/local/bin "$CONFIG_DIR"
chmod 700 "$CONFIG_DIR"
cp "$BIN_SRC" "$INSTALL_BIN"
chmod 0755 "$INSTALL_BIN"
# A binary fetched with a browser carries the quarantine flag, and launchd
# refuses to start a quarantined daemon without telling anyone why.
xattr -d com.apple.quarantine "$INSTALL_BIN" 2>/dev/null || true

if [ -n "$ENROLL_TOKEN" ]; then
    if [ -z "$SERVER_URL" ]; then
        echo "[-] An enrollment token needs the server URL as the first argument." >&2
        exit 1
    fi
    echo "[*] Enrolling with $SERVER_URL..."
    if [ -n "$SITE" ]; then
        "$INSTALL_BIN" -server "$SERVER_URL" -enroll "$ENROLL_TOKEN" -site "$SITE" -enroll-only -creds "$CREDS_PATH"
    else
        "$INSTALL_BIN" -server "$SERVER_URL" -enroll "$ENROLL_TOKEN" -enroll-only -creds "$CREDS_PATH"
    fi
elif [ ! -f "$CREDS_PATH" ]; then
    echo "[!] No enrollment token given and $CREDS_PATH does not exist."
    echo "    The daemon starts as soon as the device is enrolled:"
    echo "    sudo $INSTALL_BIN -server <url> -enroll <token> -enroll-only -creds $CREDS_PATH"
fi

cat > "$PLIST" <<PLISTEOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>$LABEL</string>
	<key>ProgramArguments</key>
	<array>
		<string>$INSTALL_BIN</string>
		<string>-creds</string>
		<string>$CREDS_PATH</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>PathState</key>
		<dict>
			<key>$CREDS_PATH</key>
			<true/>
		</dict>
	</dict>
	<key>StandardOutPath</key>
	<string>/var/log/endpoint-agent.log</string>
	<key>StandardErrorPath</key>
	<string>/var/log/endpoint-agent.err</string>
</dict>
</plist>
PLISTEOF
chmod 644 "$PLIST"
chown root:wheel "$PLIST"

launchctl bootstrap system "$PLIST" 2>/dev/null || launchctl load -w "$PLIST"
launchctl enable "system/$LABEL" 2>/dev/null || true
sleep 2

if launchctl print "system/$LABEL" 2>/dev/null | grep -q "state = running"; then
    echo "[+] LaunchDaemon $LABEL is RUNNING."
else
    echo "[*] LaunchDaemon $LABEL is loaded; it runs once $CREDS_PATH exists."
fi
echo "=========================================================="
echo "[+] Agent installation complete. Logs: /var/log/endpoint-agent.log"
echo "=========================================================="
