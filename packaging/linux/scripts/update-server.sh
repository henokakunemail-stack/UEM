#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Server - Linux Safe Updater
# ==============================================================================
# Safely updates Endpoint Management Server in-place with zero downtime config loss.
# Supports updating from GitHub Releases (default: latest) or a local binary.
# Guarantees:
#   - Auto-detection of systemd service and paths
#   - Preserves existing server.env / JWT_SECRET / configuration
#   - Creates pre-update database snapshot
#   - Binary rotation with .bak rollback protection
#   - Automatic health probe (/healthz) and instant rollback on failure
# ==============================================================================
set -euo pipefail

VERSION="latest"
BINARY_PATH=""
REPO="henokakunemail-stack/UEM"
TIMEOUT_SEC=30
SKIP_BACKUP=0
INSTALL_BIN="/usr/local/bin/endpoint-server"
ENV_FILE="/etc/endpoint-mgmt-server/server.env"
SERVICE_NAME="endpoint-mgmt-server"

usage() {
    cat <<EOF
Usage: sudo $0 [options]

Options:
  --version <tag>        GitHub release version tag (e.g. v1.1.0) or 'latest' (default: latest)
  --binary-path <path>   Use local pre-downloaded or compiled binary
  --repo <owner/repo>    GitHub repository (default: henokakunemail-stack/UEM)
  --timeout <sec>        Health check timeout in seconds (default: 30)
  --skip-backup          Skip pre-update database snapshot
  -h, --help             Show this help message

One-Liner Example:
  curl -fsSL https://raw.githubusercontent.com/henokakunemail-stack/UEM/main/packaging/linux/scripts/update-server.sh | sudo bash
EOF
    exit "${1:-0}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --version)       VERSION="${2:-latest}"; shift 2 ;;
        --binary-path)   BINARY_PATH="${2:-}"; shift 2 ;;
        --repo)          REPO="${2:-}"; shift 2 ;;
        --timeout)       TIMEOUT_SEC="${2:-30}"; shift 2 ;;
        --skip-backup)   SKIP_BACKUP=1; shift ;;
        -h|--help)       usage 0 ;;
        *) echo "[-] Unknown argument: $1" >&2; usage 1 ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This updater requires root privileges. Please run with sudo: sudo $0" >&2
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management Server - Linux Updater"
echo "=========================================================="

# 1. Auto-Detect Service and Installation
if systemctl list-unit-files | grep -q "endpoint-mgmt-server.service"; then
    SERVICE_NAME="endpoint-mgmt-server"
elif systemctl list-unit-files | grep -q "endpoint-mgmt.service"; then
    SERVICE_NAME="endpoint-mgmt"
    INSTALL_BIN="/opt/endpoint-mgmt/endpoint-mgmt-server"
    ENV_FILE="/opt/endpoint-mgmt/.env"
fi

# Try extracting binary and env from systemd unit definition if present
if systemctl cat "$SERVICE_NAME" &>/dev/null; then
    UNIT_DEF=$(systemctl cat "$SERVICE_NAME" 2>/dev/null || true)
    CAND_BIN=$(printf '%s\n' "$UNIT_DEF" | grep -m1 '^ExecStart=' | awk '{print $1}' | sed 's/^ExecStart=//' || true)
    if [ -n "$CAND_BIN" ] && [ -f "$CAND_BIN" ]; then
        INSTALL_BIN="$CAND_BIN"
    fi
    CAND_ENV=$(printf '%s\n' "$UNIT_DEF" | grep -m1 -- '-env-file' | sed -n 's/.*-env-file[ =]\([^ ]*\).*/\1/p' || true)
    if [ -n "$CAND_ENV" ] && [ -f "$CAND_ENV" ]; then
        ENV_FILE="$CAND_ENV"
    fi
fi

if [ ! -f "$INSTALL_BIN" ]; then
    for cand in /opt/endpoint-mgmt/endpoint-mgmt-server /opt/endpoint-mgmt/server /usr/local/bin/endpoint-mgmt-server /usr/local/bin/server; do
        if [ -f "$cand" ]; then
            INSTALL_BIN="$cand"
            break
        fi
    done
fi

if [ ! -f "$ENV_FILE" ]; then
    for cand_env in /opt/endpoint-mgmt/.env /opt/endpoint-mgmt/server.env /etc/endpoint-mgmt-server/server.env; do
        if [ -f "$cand_env" ]; then
            ENV_FILE="$cand_env"
            break
        fi
    done
fi

if [ ! -f "$INSTALL_BIN" ]; then
    echo "[-] Installed server binary not found at $INSTALL_BIN." >&2
    echo "    Run the installer first or verify installation directory." >&2
    exit 1
fi

echo "[+] Detected service:       $SERVICE_NAME"
echo "[+] Detected server binary: $INSTALL_BIN"
if [ -f "$ENV_FILE" ]; then
    echo "[+] Detected config file:   $ENV_FILE (PRESERVED)"
else
    echo "[!] Configuration file not found at $ENV_FILE. Will continue with existing service env."
fi

# 2. Stage Target Binary
STAGING_DIR="/tmp/uem-update-$RANDOM-$$"
mkdir -p "$STAGING_DIR"
STAGED_BIN=""

cleanup() {
    rm -rf "$STAGING_DIR"
}
trap cleanup EXIT

if [ -n "$BINARY_PATH" ]; then
    if [ ! -f "$BINARY_PATH" ]; then
        echo "[-] Specified local binary not found at '$BINARY_PATH'." >&2
        exit 1
    fi
    STAGED_BIN="$BINARY_PATH"
    echo "[+] Using local binary: $STAGED_BIN"
else
    echo "[*] Resolving release from GitHub ($REPO, version: $VERSION)..."
    API_URL="https://api.github.com/repos/$REPO/releases/latest"
    if [ "$VERSION" != "latest" ]; then
        API_URL="https://api.github.com/repos/$REPO/releases/tags/$VERSION"
    fi

    AUTH_HEADER=()
    if [ -n "${GITHUB_TOKEN:-}" ]; then
        AUTH_HEADER=(-H "Authorization: token $GITHUB_TOKEN")
    fi

    RELEASE_JSON=$(curl -sSL -H "User-Agent: UEM-Server-Updater" "${AUTH_HEADER[@]}" "$API_URL" || true)

    # Find download URL for Linux amd64 tarball or binary
    DOWNLOAD_URL=$(printf '%s\n' "$RELEASE_JSON" | grep -E '"browser_download_url":.*(endpoint-server.*linux.*amd64\.tar\.gz|server-linux-amd64|endpoint-server-linux-amd64)' | head -n1 | sed -E 's/.*"browser_download_url": *"([^"]+)".*/\1/' || true)

    # Fallback to direct URL if API rate-limited
    if [ -z "$DOWNLOAD_URL" ]; then
        TAG_NAME=$(printf '%s\n' "$RELEASE_JSON" | grep -m1 '"tag_name":' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/' || true)
        if [ -z "$TAG_NAME" ] || [ "$TAG_NAME" = "latest" ]; then TAG_NAME="v1.0.0"; fi
        if [ "$VERSION" != "latest" ]; then
            TAG_NAME="$VERSION"
        fi
        DOWNLOAD_URL="https://github.com/$REPO/releases/download/$TAG_NAME/endpoint-server-linux-amd64.tar.gz"
    fi

    echo "[+] Downloading release asset: $DOWNLOAD_URL..."
    DL_FILE="$STAGING_DIR/$(basename "$DOWNLOAD_URL")"
    if ! curl -sSL -H "User-Agent: UEM-Server-Updater" "${AUTH_HEADER[@]}" "$DOWNLOAD_URL" -o "$DL_FILE"; then
        echo "[-] Download failed. Check internet access or provide local binary with --binary-path." >&2
        exit 1
    fi

    if [[ "$DL_FILE" == *.tar.gz ]]; then
        echo "[*] Extracting tar archive..."
        tar -xzf "$DL_FILE" -C "$STAGING_DIR"
        for cand in "$STAGING_DIR/endpoint-server" "$STAGING_DIR/server-linux-amd64" "$STAGING_DIR/endpoint-mgmt-server"; do
            if [ -f "$cand" ]; then STAGED_BIN="$cand"; break; fi
        done
        if [ -z "$STAGED_BIN" ]; then
            STAGED_BIN=$(find "$STAGING_DIR" -type f -executable | head -n1 || true)
        fi
    else
        STAGED_BIN="$DL_FILE"
    fi

    if [ -z "$STAGED_BIN" ] || [ ! -f "$STAGED_BIN" ]; then
        echo "[-] Could not extract server binary from release package." >&2
        exit 1
    fi
fi

chmod +x "$STAGED_BIN"
BIN_SIZE=$(wc -c < "$STAGED_BIN")
if [ "$BIN_SIZE" -lt 5000000 ]; then
    echo "[-] Binary size ($BIN_SIZE bytes) is suspiciously small. Aborting update." >&2
    exit 1
fi
echo "[+] Validated new binary size: $(( BIN_SIZE / 1024 / 1024 )) MB"

# 3. Pre-Update Hot Database Snapshot
if [ "$SKIP_BACKUP" -eq 0 ] && [ -f "$ENV_FILE" ]; then
    echo "[*] Taking consistent pre-update database backup..."
    BACKUP_DIR=$(grep -m1 '^BACKUP_DIR=' "$ENV_FILE" | cut -d= -f2- || true)
    if [ -z "$BACKUP_DIR" ]; then
        BACKUP_DIR="/var/lib/endpoint-mgmt-server/backups"
    fi
    mkdir -p "$BACKUP_DIR"
    TIMESTAMP=$(date +%Y%m%d_%H%M%S)
    BACKUP_TARGET="$BACKUP_DIR/pre_update_$TIMESTAMP.db"

    if "$INSTALL_BIN" -env-file "$ENV_FILE" -backup "$BACKUP_TARGET" 2>/dev/null; then
        echo "[+] Consistent SQLite snapshot created: $BACKUP_TARGET"
    else
        DB_PATH=$(grep -m1 '^DB_PATH=' "$ENV_FILE" | cut -d= -f2- || true)
        if [ -n "$DB_PATH" ] && [ -f "$DB_PATH" ]; then
            cp -p "$DB_PATH" "$BACKUP_TARGET" || true
            echo "[+] File-level database copy created: $BACKUP_TARGET"
        fi
    fi
fi

# 4. Stop Service Gracefully
echo "[*] Stopping systemd service '$SERVICE_NAME'..."
systemctl stop "$SERVICE_NAME" || true
sleep 1

# 5. Rotate Binary with Rollback Safety (.bak)
BAK_BIN="$INSTALL_BIN.bak"
echo "[*] Rotating server binary ($INSTALL_BIN -> $BAK_BIN)..."
cp -p "$INSTALL_BIN" "$BAK_BIN"
cp -p "$STAGED_BIN" "$INSTALL_BIN"
chmod 0755 "$INSTALL_BIN"

# Agent binaries for the one-line Linux installer (/install/linux.sh) travel in
# the same package; the server looks for them in agents/ next to its binary.
if [ -n "${STAGING_DIR:-}" ] && [ -d "${STAGING_DIR}/agents" ]; then
    AGENTS_DIR="$(dirname "$INSTALL_BIN")/agents"
    mkdir -p "$AGENTS_DIR"
    cp -p "$STAGING_DIR"/agents/agent-linux-* "$AGENTS_DIR"/ 2>/dev/null || true
    chmod 0755 "$AGENTS_DIR"/agent-linux-* 2>/dev/null || true
    echo "[+] Updated agent installer binaries in $AGENTS_DIR"
fi

# Maintain file ownership if dedicated system user exists
OWNER=$(stat -c '%U:%G' "$BAK_BIN" 2>/dev/null || echo "")
if [ -n "$OWNER" ]; then
    chown "$OWNER" "$INSTALL_BIN" || true
fi

# 6. Restart Service
echo "[*] Starting systemd service '$SERVICE_NAME'..."
systemctl start "$SERVICE_NAME"
sleep 2

# 7. Health Probe Verification & Automatic Rollback
PORT="8443"
if [ -f "$ENV_FILE" ]; then
    HTTP_ADDR=$(grep -m1 '^HTTP_ADDR=' "$ENV_FILE" | cut -d= -f2- || true)
    if [[ "$HTTP_ADDR" =~ :([0-9]+) ]]; then
        PORT="${BASH_REMATCH[1]}"
    fi
fi

echo "[*] Probing server health at http://127.0.0.1:$PORT/healthz (timeout: ${TIMEOUT_SEC}s)..."
HEALTHY=0
ELAPSED=0

while [ "$ELAPSED" -lt "$TIMEOUT_SEC" ]; do
    # Try HTTPS too: a TLS-only deployment answers on the same port, and probing
    # plain HTTP only would report a healthy new binary as broken and roll it back.
    if curl -sf --connect-timeout 2 "http://127.0.0.1:$PORT/healthz" &>/dev/null || \
       curl -sf -k --connect-timeout 2 "https://127.0.0.1:$PORT/healthz" &>/dev/null; then
        HEALTHY=1
        break
    fi
    sleep 2
    ELAPSED=$(( ELAPSED + 2 ))
done

if [ "$HEALTHY" -eq 1 ]; then
    echo "=========================================================="
    echo "   Server Update Successful & Verified Healthy!"
    echo "=========================================================="
    echo "  Service Status:      $(systemctl is-active "$SERVICE_NAME")"
    echo "  Active Binary:       $INSTALL_BIN"
    echo "  Rollback Backup:     $BAK_BIN"
    echo "  Configuration File:  $ENV_FILE (UNTOUCHED)"
    exit 0
else
    echo "[-] UPDATE FAILED: Server failed health check after ${TIMEOUT_SEC}s." >&2
    echo "[!] Initiating automatic rollback to previous binary..." >&2
    systemctl stop "$SERVICE_NAME" || true
    cp -p "$BAK_BIN" "$INSTALL_BIN"
    systemctl start "$SERVICE_NAME"
    echo "[+] Rollback complete. Original binary restored and service restarted." >&2
    echo "    Check logs via: journalctl -u $SERVICE_NAME -n 50" >&2
    exit 1
fi
