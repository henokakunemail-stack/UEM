#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management — Ubuntu Server Production Safe Updater
# ==============================================================================
# In-place production updater for Ubuntu / Debian servers deployed via deploy/
# Guarantees:
#   - Auto-detects /opt/endpoint-mgmt deployment
#   - Preserves /opt/endpoint-mgmt/.env and all TLS certs / Nginx settings
#   - Takes pre-update SQLite hot backup in /opt/endpoint-mgmt/data/backups
#   - Binary swap with rollback protection (.bak)
#   - Verifies backend (/healthz) and restarts daemon cleanly
# ==============================================================================
set -euo pipefail

VERSION="latest"
BINARY_PATH=""
REPO="henokakunemail-stack/UEM"
TIMEOUT_SEC=30
SKIP_BACKUP=0
INSTALL_DIR="/opt/endpoint-mgmt"
SERVICE_NAME="endpoint-mgmt"
SERVICE_USER="endpointmgmt"

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
  curl -fsSL https://raw.githubusercontent.com/henokakunemail-stack/UEM/main/deploy/update-server.sh | sudo bash
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
    echo "[-] This updater requires root privileges (use sudo)." >&2
    exit 1
fi

echo "=================================================================="
echo "  ENDPOINT MANAGEMENT SERVER PRODUCTION UPDATER (UBUNTU / DEBIAN)"
echo "=================================================================="

INSTALL_BIN="$INSTALL_DIR/endpoint-mgmt-server"
ENV_FILE="$INSTALL_DIR/.env"
DATA_DIR="$INSTALL_DIR/data"
BACKUP_DIR="$DATA_DIR/backups"

if [ ! -f "$INSTALL_BIN" ]; then
    echo "[-] Server binary not found at $INSTALL_BIN." >&2
    echo "    Is the server deployed using deploy/install-ubuntu.sh?" >&2
    exit 1
fi

echo "[+] Install root:   $INSTALL_DIR"
echo "[+] Active binary:  $INSTALL_BIN"
echo "[+] Config file:    $ENV_FILE (PRESERVED)"

STAGING_DIR="/tmp/uem-ubuntu-update-$RANDOM-$$"
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

    RELEASE_JSON=$(curl -sSL -H "User-Agent: UEM-Ubuntu-Updater" "${AUTH_HEADER[@]}" "$API_URL" || true)
    DOWNLOAD_URL=$(printf '%s\n' "$RELEASE_JSON" | grep -E '"browser_download_url":.*(endpoint-server.*linux.*amd64\.tar\.gz|server-linux-amd64|endpoint-mgmt-server-linux-amd64)' | head -n1 | sed -E 's/.*"browser_download_url": *"([^"]+)".*/\1/' || true)

    if [ -z "$DOWNLOAD_URL" ]; then
        TAG_NAME="$VERSION"
        if [ "$TAG_NAME" = "latest" ]; then TAG_NAME="v1.0.0"; fi
        DOWNLOAD_URL="https://github.com/$REPO/releases/download/$TAG_NAME/endpoint-server-linux-amd64.tar.gz"
    fi

    echo "[+] Downloading release asset: $DOWNLOAD_URL..."
    DL_FILE="$STAGING_DIR/$(basename "$DOWNLOAD_URL")"
    if ! curl -sSL -H "User-Agent: UEM-Ubuntu-Updater" "${AUTH_HEADER[@]}" "$DOWNLOAD_URL" -o "$DL_FILE"; then
        echo "[-] Download failed. Check internet access or pass --binary-path." >&2
        exit 1
    fi

    if [[ "$DL_FILE" == *.tar.gz ]]; then
        echo "[*] Extracting tar archive..."
        tar -xzf "$DL_FILE" -C "$STAGING_DIR"
        for cand in "$STAGING_DIR/endpoint-mgmt-server" "$STAGING_DIR/endpoint-server" "$STAGING_DIR/server-linux-amd64"; do
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

# 1. Hot database backup
if [ "$SKIP_BACKUP" -eq 0 ] && [ -f "$ENV_FILE" ]; then
    echo "[*] Creating pre-update SQLite database snapshot..."
    mkdir -p "$BACKUP_DIR"
    TIMESTAMP=$(date +%Y%m%d_%H%M%S)
    BACKUP_TARGET="$BACKUP_DIR/pre_update_$TIMESTAMP.db"

    if "$INSTALL_BIN" -env-file "$ENV_FILE" -backup "$BACKUP_TARGET" 2>/dev/null; then
        echo "[+] Consistent snapshot created: $BACKUP_TARGET"
    else
        DB_PATH=$(grep -m1 '^DB_PATH=' "$ENV_FILE" | cut -d= -f2- || echo "$DATA_DIR/endpoint-mgmt.db")
        if [ -f "$DB_PATH" ]; then
            cp -p "$DB_PATH" "$BACKUP_TARGET" || true
            echo "[+] File-level database copy created: $BACKUP_TARGET"
        fi
    fi
    chown -R "$SERVICE_USER:$SERVICE_USER" "$BACKUP_DIR" 2>/dev/null || true
fi

# 2. Stop service
echo "[*] Stopping systemd service '$SERVICE_NAME'..."
systemctl stop "$SERVICE_NAME" || true
sleep 1

# 3. Swap binary with .bak
BAK_BIN="$INSTALL_BIN.bak"
echo "[*] Rotating server binary ($INSTALL_BIN -> $BAK_BIN)..."
cp -p "$INSTALL_BIN" "$BAK_BIN"
cp -p "$STAGED_BIN" "$INSTALL_BIN"
chmod 0755 "$INSTALL_BIN"
chown "$SERVICE_USER:$SERVICE_USER" "$INSTALL_BIN"

# 4. Restart service
echo "[*] Starting systemd service '$SERVICE_NAME'..."
systemctl start "$SERVICE_NAME"
sleep 2

# 5. Verify /healthz
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
    if curl -sf --connect-timeout 2 "http://127.0.0.1:$PORT/healthz" &>/dev/null; then
        HEALTHY=1
        break
    fi
    sleep 2
    ELAPSED=$(( ELAPSED + 2 ))
done

if [ "$HEALTHY" -eq 1 ]; then
    echo "=================================================================="
    echo "  PRODUCTION SERVER UPDATE SUCCESSFUL & VERIFIED HEALTHY!"
    echo "=================================================================="
    echo "  Service Status:      $(systemctl is-active "$SERVICE_NAME")"
    echo "  Active Binary:       $INSTALL_BIN"
    echo "  Rollback Backup:     $BAK_BIN"
    echo "  Configuration File:  $ENV_FILE (UNTOUCHED)"
    exit 0
else
    echo "[-] UPDATE FAILED: Health probe timed out after ${TIMEOUT_SEC}s." >&2
    echo "[!] Initiating automatic rollback to previous binary..." >&2
    systemctl stop "$SERVICE_NAME" || true
    cp -p "$BAK_BIN" "$INSTALL_BIN"
    chown "$SERVICE_USER:$SERVICE_USER" "$INSTALL_BIN"
    systemctl start "$SERVICE_NAME"
    echo "[+] Rollback complete. Original binary restored and service restarted." >&2
    echo "    Check logs via: journalctl -u $SERVICE_NAME -n 50" >&2
    exit 1
fi
