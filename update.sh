#!/usr/bin/env bash
# ==============================================================================
# Enterprise Endpoint Management Server - Direct GitHub One-Liner Updater
# ==============================================================================
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/henokakunemail-stack/UEM/main/update.sh | sudo bash
#
# Options:
#   --branch <branch>     Target branch to update from (default: main)
#   --binary-path <path>  Use a pre-downloaded or local binary
#   --skip-backup         Skip database backup
#   --timeout <sec>       Health probe timeout in seconds (default: 30)
# ==============================================================================
set -euo pipefail

REPO="henokakunemail-stack/UEM"
BRANCH="main"
BINARY_PATH=""
TIMEOUT_SEC=30
SKIP_BACKUP=0

usage() {
    cat <<EOF
Enterprise Endpoint Management Server - Direct GitHub Updater

Usage: sudo $0 [options]

Options:
  --branch <name>       Git branch to update from (default: main)
  --binary-path <path>  Use local or pre-downloaded binary
  --timeout <sec>       Health probe timeout in seconds (default: 30)
  --skip-backup         Skip pre-update database backup
  -h, --help            Show this help message

One-Liner Execution:
  curl -fsSL https://raw.githubusercontent.com/henokakunemail-stack/UEM/main/update.sh | sudo bash
EOF
    exit "${1:-0}"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --branch)       BRANCH="${2:-main}"; shift 2 ;;
        --binary-path)   BINARY_PATH="${2:-}"; shift 2 ;;
        --timeout)       TIMEOUT_SEC="${2:-30}"; shift 2 ;;
        --skip-backup)   SKIP_BACKUP=1; shift ;;
        -h|--help)       usage 0 ;;
        *) echo "[-] Unknown argument: $1" >&2; usage 1 ;;
    esac
done

if [ "$(id -u)" -ne 0 ]; then
    echo "[-] This updater requires root privileges. Please run with sudo:" >&2
    echo "    curl -fsSL https://raw.githubusercontent.com/henokakunemail-stack/UEM/main/update.sh | sudo bash" >&2
    exit 1
fi

echo "=========================================================="
echo "   Enterprise Endpoint Management - Server Updater"
echo "=========================================================="

# 1. Auto-Detect Service & Paths
SERVICE_NAME=""
INSTALL_BIN=""
ENV_FILE=""

CANDIDATE_SERVICES=("endpoint-mgmt-server" "endpoint-mgmt" "endpoint-server" "uem-server")
for svc in "${CANDIDATE_SERVICES[@]}"; do
    if systemctl list-unit-files | grep -q "^${svc}.service"; then
        SERVICE_NAME="$svc"
        break
    fi
done

if [ -n "$SERVICE_NAME" ] && systemctl cat "$SERVICE_NAME" &>/dev/null; then
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

# Fallback path probing
if [ -z "$INSTALL_BIN" ]; then
    for cand in "/usr/local/bin/endpoint-server" "/opt/endpoint-mgmt/endpoint-mgmt-server" "/opt/endpoint-manager/endpoint-server" "/usr/bin/endpoint-server"; do
        if [ -f "$cand" ]; then
            INSTALL_BIN="$cand"
            break
        fi
    done
fi

if [ -z "$ENV_FILE" ]; then
    for cand in "/etc/endpoint-mgmt-server/server.env" "/opt/endpoint-mgmt/.env" "/opt/endpoint-manager/.env" "/etc/endpoint-manager/server.env"; do
        if [ -f "$cand" ]; then
            ENV_FILE="$cand"
            break
        fi
    done
fi

if [ -z "$INSTALL_BIN" ]; then
    echo "[-] Installed server binary not found in standard paths." >&2
    echo "    Please verify service installation or supply target via installation script." >&2
    exit 1
fi

echo "[+] Service:        ${SERVICE_NAME:-manual/stand-alone}"
echo "[+] Binary path:    $INSTALL_BIN"
if [ -n "$ENV_FILE" ]; then
    echo "[+] Configuration:  $ENV_FILE (PRESERVED)"
fi

# 2. Prepare Staging Area
STAGING_DIR=$(mktemp -d "/tmp/uem-update-XXXXXX")
cleanup() {
    rm -rf "$STAGING_DIR"
}
trap cleanup EXIT
STAGED_BIN=""

if [ -n "$BINARY_PATH" ]; then
    if [ ! -f "$BINARY_PATH" ]; then
        echo "[-] Specified local binary not found: $BINARY_PATH" >&2
        exit 1
    fi
    STAGED_BIN="$BINARY_PATH"
    echo "[+] Using provided binary: $STAGED_BIN"
else
    # Check GitHub Releases first
    echo "[*] Checking for latest release from GitHub ($REPO)..."
    API_URL="https://api.github.com/repos/$REPO/releases/latest"
    RELEASE_JSON=$(curl -sSL -H "User-Agent: UEM-Updater" "$API_URL" || true)

    DOWNLOAD_URL=$(printf '%s\n' "$RELEASE_JSON" | grep -E '"browser_download_url":.*(server-linux-amd64|endpoint-server.*linux.*amd64)' | head -n1 | sed -E 's/.*"browser_download_url": *"([^"]+)".*/\1/' || true)

    # An anonymous GitHub API call is rate limited hard; fall back to the direct
    # download URL derived from the tag the API did report.
    if [ -z "$DOWNLOAD_URL" ]; then
        REPORTED_TAG=$(printf '%s\n' "$RELEASE_JSON" | grep -m1 '"tag_name":' | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/' || true)
        if [ -n "$REPORTED_TAG" ]; then
            DOWNLOAD_URL="https://github.com/$REPO/releases/download/$REPORTED_TAG/server-linux-amd64"
        fi
    fi

    if [ -n "$DOWNLOAD_URL" ]; then
        echo "[+] Found release asset: $DOWNLOAD_URL"
        DL_TARGET="$STAGING_DIR/$(basename "$DOWNLOAD_URL")"
        if curl -sSL -H "User-Agent: UEM-Updater" "$DOWNLOAD_URL" -o "$DL_TARGET"; then
            if [[ "$DL_TARGET" == *.tar.gz ]]; then
                tar -xzf "$DL_TARGET" -C "$STAGING_DIR"
                STAGED_BIN=$(find "$STAGING_DIR" -type f -executable | head -n1 || true)
            else
                STAGED_BIN="$DL_TARGET"
            fi
        fi
    fi

    # Fallback: Git Source Auto-Build
    if [ -z "$STAGED_BIN" ] || [ ! -f "$STAGED_BIN" ]; then
        echo "[*] No release binary asset available on GitHub releases."
        echo "[*] Falling back to repository source build from branch: $BRANCH..."

        REPO_DIR=""
        BIN_DIR=$(dirname "$INSTALL_BIN")
        if [ -d "$BIN_DIR/.git" ]; then
            REPO_DIR="$BIN_DIR"
        elif [ -d "/opt/endpoint-mgmt/.git" ]; then
            REPO_DIR="/opt/endpoint-mgmt"
        elif [ -d "$PWD/.git" ]; then
            REPO_DIR="$PWD"
        fi

        if [ -n "$REPO_DIR" ]; then
            echo "[+] Detected existing git repository at: $REPO_DIR"
            cd "$REPO_DIR"
            git fetch origin "$BRANCH"
            git checkout "$BRANCH"
            git pull origin "$BRANCH"
            SRC_DIR="$REPO_DIR"
        else
            echo "[*] Cloning latest source code into staging..."
            git clone --depth 1 --branch "$BRANCH" "https://github.com/$REPO.git" "$STAGING_DIR/repo"
            SRC_DIR="$STAGING_DIR/repo"
        fi

        if ! command -v go >/dev/null 2>&1; then
            echo "[-] Go compiler not found on this system to compile from source." >&2
            echo "    Install Go via 'apt install golang-go' or publish release assets on GitHub." >&2
            exit 1
        fi

        echo "[*] Compiling server binary (embedded web console is pre-built in repository)..."
        cd "$SRC_DIR"
        CGO_ENABLED=0 go build -ldflags="-s -w" -o "$STAGING_DIR/endpoint-server" ./server/cmd/server
        STAGED_BIN="$STAGING_DIR/endpoint-server"
    fi
fi

chmod +x "$STAGED_BIN"
BIN_SIZE=$(wc -c < "$STAGED_BIN")
if [ "$BIN_SIZE" -lt 5000000 ]; then
    echo "[-] Binary size ($BIN_SIZE bytes) is suspiciously small. Aborting." >&2
    exit 1
fi
echo "[+] Validated new server binary ($(( BIN_SIZE / 1024 / 1024 )) MB)"

# 3. Database Snapshot Backup
if [ "$SKIP_BACKUP" -eq 0 ]; then
    DB_PATH=""
    if [ -n "$ENV_FILE" ] && [ -f "$ENV_FILE" ]; then
        DB_PATH=$(grep -m1 '^DB_PATH=' "$ENV_FILE" | cut -d= -f2- | tr -d '"' | tr -d "'" || true)
    fi
    # Fallbacks cover the default layout written by the systemd installer.
    if [ -z "$DB_PATH" ]; then
        for cand in "/var/lib/endpoint-mgmt-server/server.db" \
                    "/var/lib/endpoint-mgmt-server/endpoint-manager.db" \
                    "/opt/endpoint-mgmt/data/server.db" \
                    "/opt/endpoint-manager/data/endpoint-manager.db"; do
            if [ -f "$cand" ]; then DB_PATH="$cand"; break; fi
        done
    fi

    if [ -n "$DB_PATH" ] && [ -f "$DB_PATH" ]; then
        BACKUP_FILE="${DB_PATH}.bak.$(date +%Y%m%d%H%M%S)"
        echo "[*] Creating database snapshot: $BACKUP_FILE..."
        # Prefer the server binary's own VACUUM INTO backup: it checkpoints WAL
        # and is safe while the service is still running. sqlite3 CLI is the
        # second choice; a cold copy is last resort and stops the service first.
        if [ -n "$INSTALL_BIN" ] && \
           "$INSTALL_BIN" -env-file "$ENV_FILE" -backup "$BACKUP_FILE" 2>/dev/null; then
            :
        elif command -v sqlite3 >/dev/null 2>&1; then
            sqlite3 "$DB_PATH" ".backup '$BACKUP_FILE'"
        else
            if [ -n "$SERVICE_NAME" ]; then systemctl stop "$SERVICE_NAME" || true; fi
            cp "$DB_PATH" "$BACKUP_FILE"
            [ -f "${DB_PATH}-wal" ] && cp "${DB_PATH}-wal" "${BACKUP_FILE}-wal" || true
            [ -f "${DB_PATH}-shm" ] && cp "${DB_PATH}-shm" "${BACKUP_FILE}-shm" || true
            if [ -n "$SERVICE_NAME" ]; then systemctl start "$SERVICE_NAME" || true; fi
        fi
        # The service user must own the snapshot, or the service cannot read or
        # rotate it later.
        if [ -n "$SERVICE_NAME" ]; then
            SERVICE_USER=$(systemctl show -pUser --value "$SERVICE_NAME" 2>/dev/null || true)
            if [ -n "$SERVICE_USER" ] && [ "$SERVICE_USER" != "" ]; then
                chown "$SERVICE_USER" "$BACKUP_FILE" 2>/dev/null || true
            fi
        fi
        echo "[+] Database backed up successfully."
    else
        echo "[!] No database file located; continuing without snapshot."
    fi
fi

# 4. Stop Service & Swap Binary
if [ -n "$SERVICE_NAME" ]; then
    echo "[*] Stopping service $SERVICE_NAME..."
    systemctl stop "$SERVICE_NAME"
fi

echo "[*] Rotating server binary (creating ${INSTALL_BIN}.bak)..."
# Preserve the original ownership and mode: a binary installed as an
# unprivileged service user would otherwise be rotated to root:root.
ORIG_OWNER=$(stat -c '%U:%G' "$INSTALL_BIN" 2>/dev/null || true)
cp -p "$INSTALL_BIN" "${INSTALL_BIN}.bak"
cp "$STAGED_BIN" "$INSTALL_BIN"
chmod 0755 "$INSTALL_BIN"
if [ -n "$ORIG_OWNER" ] && [ "$ORIG_OWNER" != "" ]; then
    chown "$ORIG_OWNER" "$INSTALL_BIN" 2>/dev/null || true
fi

# 5. Restart Service & Health Check
if [ -n "$SERVICE_NAME" ]; then
    echo "[*] Starting service $SERVICE_NAME..."
    systemctl start "$SERVICE_NAME"
fi

# Determine HTTP Port from Env
PORT=8443
if [ -n "$ENV_FILE" ] && [ -f "$ENV_FILE" ]; then
    HTTP_ADDR=$(grep -m1 '^HTTP_ADDR=' "$ENV_FILE" | cut -d= -f2- | tr -d '"' | tr -d "'" || true)
    if [[ "$HTTP_ADDR" =~ :([0-9]+)$ ]]; then
        PORT="${BASH_REMATCH[1]}"
    fi
fi

echo "[*] Running health check on http://127.0.0.1:$PORT/healthz (timeout: ${TIMEOUT_SEC}s)..."
HEALTH_OK=0
ELAPSED=0
while [ "$ELAPSED" -lt "$TIMEOUT_SEC" ]; do
    # Try both plain HTTP and HTTPS (self-signed) so a TLS-only deployment is
    # not misjudged as a failed boot and rolled back.
    if curl -s -f -m 3 "http://127.0.0.1:$PORT/healthz" >/dev/null 2>&1 || \
       curl -s -f -k -m 3 "https://127.0.0.1:$PORT/healthz" >/dev/null 2>&1; then
        HEALTH_OK=1
        break
    fi
    sleep 1
    ELAPSED=$((ELAPSED + 1))
done

if [ "$HEALTH_OK" -eq 1 ]; then
    echo "=========================================================="
    echo "   [+] Update Completed Successfully! (Health OK)"
    echo "=========================================================="
    echo "Active binary:    $INSTALL_BIN"
    echo "Service status:   active (running)"
    echo "Web Console URL:  http://$(hostname -I | awk '{print $1}'):$PORT"
    exit 0
else
    echo "[-] Health check failed after ${TIMEOUT_SEC}s! Initiating automatic rollback..." >&2
    if [ -n "$SERVICE_NAME" ]; then
        systemctl stop "$SERVICE_NAME" || true
    fi
    cp "${INSTALL_BIN}.bak" "$INSTALL_BIN"
    if [ -n "$SERVICE_NAME" ]; then
        systemctl start "$SERVICE_NAME" || true
    fi
    echo "[-] Server rolled back to previous version. Check logs with: journalctl -u $SERVICE_NAME -n 50" >&2
    exit 1
fi
