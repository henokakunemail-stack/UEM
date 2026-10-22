#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Server - macOS Installer (LaunchDaemon)
# ==============================================================================
# Usage: sudo ./install-server.sh [listen_addr] [admin_password]
#
# Run from the unpacked endpoint-server-darwin-<arch>.tar.gz. Installs the
# server and the agents/ bundle under /usr/local/endpoint-server, keeps an
# existing server.env, and registers the com.endpoint-mgmt.server LaunchDaemon.
set -euo pipefail

LISTEN_ADDR="${1:-:8443}"
ADMIN_PASSWORD="${2:-}"
LABEL="com.endpoint-mgmt.server"
INSTALL_DIR="/usr/local/endpoint-server"
INSTALL_BIN="$INSTALL_DIR/endpoint-server"
DATA_DIR="/usr/local/var/endpoint-mgmt-server"
CONF_DIR="/usr/local/etc/endpoint-mgmt-server"
ENV_FILE="$CONF_DIR/server.env"
PLIST="/Library/LaunchDaemons/$LABEL.plist"

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This installer requires root privileges. Run: sudo $0 [listen_addr] [admin_password]" >&2
    exit 1
fi
if [ "$(uname -s)" != "Darwin" ]; then
    echo "[-] This installer is for macOS. Use packaging/linux/scripts/install-server.sh on Linux." >&2
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management Server - macOS Installer"
echo "=========================================================="

if [ "$(sysctl -n hw.optional.arm64 2>/dev/null || echo 0)" = "1" ]; then
    ARCH=arm64
else
    ARCH=amd64
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_SRC=""
for cand in "$SCRIPT_DIR/endpoint-server" "$SCRIPT_DIR/server-darwin-$ARCH" "$(pwd)/server-darwin-$ARCH"; do
    if [ -f "$cand" ]; then BIN_SRC="$cand"; break; fi
done
if [ -z "$BIN_SRC" ]; then
    echo "[-] Server binary not found. Place 'server-darwin-$ARCH' or 'endpoint-server' alongside this script." >&2
    exit 1
fi
echo "[+] Using binary: $BIN_SRC ($ARCH)"

launchctl bootout "system/$LABEL" 2>/dev/null || launchctl unload -w "$PLIST" 2>/dev/null || true

mkdir -p "$INSTALL_DIR" "$DATA_DIR/backups" "$CONF_DIR"
chmod 700 "$CONF_DIR"
if [ -f "$INSTALL_BIN" ]; then
    cp -p "$INSTALL_BIN" "$INSTALL_BIN.bak"
fi
cp "$BIN_SRC" "$INSTALL_BIN"
chmod 0755 "$INSTALL_BIN"
xattr -d com.apple.quarantine "$INSTALL_BIN" 2>/dev/null || true

# The agents for every platform ship in the same package. The server hands
# them to the one-line installer and registers them as releases from Agent
# Updates; it looks for them in agents/ next to its binary.
if [ -d "$SCRIPT_DIR/agents" ]; then
    mkdir -p "$INSTALL_DIR/agents"
    cp "$SCRIPT_DIR"/agents/agent-* "$INSTALL_DIR/agents/"
    chmod 0755 "$INSTALL_DIR"/agents/agent-*
    xattr -dr com.apple.quarantine "$INSTALL_DIR/agents" 2>/dev/null || true
    echo "[+] Installed bundled agents to $INSTALL_DIR/agents"
fi

if [ -f "$ENV_FILE" ]; then
    echo "[*] $ENV_FILE exists; keeping its settings and JWT_SECRET."
else
    echo "[*] Writing $ENV_FILE..."
    cat > "$ENV_FILE" <<ENVEOF
# Runtime configuration for endpoint-mgmt-server
HTTP_ADDR=$LISTEN_ADDR
DB_PATH=$DATA_DIR/server.db
BACKUP_DIR=$DATA_DIR/backups
LOG_FILE=$DATA_DIR/server.log
LOG_LEVEL=info
ALLOWED_ORIGIN_DOMAINS=
JWT_SECRET=
ENVEOF
    if [ -n "$ADMIN_PASSWORD" ]; then
        echo "ADMIN_PASSWORD=$ADMIN_PASSWORD" >> "$ENV_FILE"
    fi
    chmod 0600 "$ENV_FILE"
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
		<string>-env-file</string>
		<string>$ENV_FILE</string>
	</array>
	<key>WorkingDirectory</key>
	<string>$DATA_DIR</string>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>SoftResourceLimits</key>
	<dict>
		<key>NumberOfFiles</key>
		<integer>65535</integer>
	</dict>
	<key>StandardOutPath</key>
	<string>$DATA_DIR/stdout.log</string>
	<key>StandardErrorPath</key>
	<string>$DATA_DIR/stderr.log</string>
</dict>
</plist>
PLISTEOF
chmod 644 "$PLIST"
chown root:wheel "$PLIST"

launchctl bootstrap system "$PLIST" 2>/dev/null || launchctl load -w "$PLIST"
launchctl enable "system/$LABEL" 2>/dev/null || true

PORT="${LISTEN_ADDR##*:}"
for _ in $(seq 1 15); do
    if curl -fsS "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
        echo "[+] Server is RUNNING on $LISTEN_ADDR."
        break
    fi
    sleep 1
done
echo "=========================================================="
echo "[+] Server installation complete."
echo "    Config: $ENV_FILE   Data: $DATA_DIR"
echo "    Logs:   $DATA_DIR/server.log"
echo "=========================================================="
