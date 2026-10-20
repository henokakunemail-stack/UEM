#!/usr/bin/env bash
# Endpoint Manager agent installer for Linux.
#
#   curl -fsSL https://SERVER/install/linux.sh | sudo bash -s -- --server https://SERVER --token TOKEN [--site NAME]
#
# Downloads the agent for this machine's architecture from the server, checks
# its SHA-256, enrolls the device and runs the agent as the systemd service
# "endpoint-agent". Running it again upgrades the binary and keeps the
# enrollment.
set -euo pipefail

SERVER=""
TOKEN=""
SITE=""
INSECURE=0
BIN=/usr/local/bin/endpoint-agent
CONF_DIR=/etc/endpoint-agent
CREDS="$CONF_DIR/creds.json"
SERVICE=endpoint-agent

die() { echo "[-] $*" >&2; exit 1; }
info() { echo "[*] $*"; }
ok() { echo "[+] $*"; }

while [ $# -gt 0 ]; do
  case "$1" in
    --server) SERVER="${2:-}"; shift 2 ;;
    --token) TOKEN="${2:-}"; shift 2 ;;
    --site) SITE="${2:-}"; shift 2 ;;
    --insecure) INSECURE=1; shift ;;
    -h|--help) echo "usage: install-linux.sh --server URL --token TOKEN [--site NAME] [--insecure]"; exit 0 ;;
    *) die "unknown option: $1" ;;
  esac
done

[ "$(id -u)" -eq 0 ] || die "run as root: curl ... | sudo bash -s -- --server URL --token TOKEN"
[ -n "$SERVER" ] || die "--server is required (the console's address, e.g. https://uem.example.com)"
SERVER="${SERVER%/}"
command -v systemctl >/dev/null 2>&1 || die "systemd is required; this system has no systemctl"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported architecture $(uname -m); supported: x86_64, aarch64" ;;
esac

ENROLLED=0
if [ -s "$CREDS" ]; then
  ENROLLED=1
elif [ -z "$TOKEN" ]; then
  die "--token is required for a first install (create one under Devices > Enroll device)"
fi
# A reinstall on an enrolled machine still needs a token to download.
[ -n "$TOKEN" ] || die "--token is required to download the agent"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
URL="$SERVER/install/agent/linux/$ARCH"
info "Downloading the agent for linux/$ARCH from $SERVER"
if command -v curl >/dev/null 2>&1; then
  CURL_OPTS=(-fsS --retry 3 -D "$TMP/headers" -o "$TMP/agent" -H "X-Enrollment-Token: $TOKEN")
  [ "$INSECURE" -eq 1 ] && CURL_OPTS+=(-k)
  if ! curl "${CURL_OPTS[@]}" "$URL"; then
    # Ask again without -f to show the server's reason.
    REASON_OPTS=(-sS -H "X-Enrollment-Token: $TOKEN")
    [ "$INSECURE" -eq 1 ] && REASON_OPTS+=(-k)
    REASON="$(curl "${REASON_OPTS[@]}" "$URL" 2>&1 | head -c 400 || true)"
    die "download failed: ${REASON:-no response from $SERVER}"
  fi
elif command -v wget >/dev/null 2>&1; then
  WGET_OPTS=(-q -S -O "$TMP/agent" --header "X-Enrollment-Token: $TOKEN")
  [ "$INSECURE" -eq 1 ] && WGET_OPTS+=(--no-check-certificate)
  wget "${WGET_OPTS[@]}" "$URL" 2>"$TMP/headers" || die "download failed (wget); check the token and the server address"
else
  die "curl or wget is required"
fi

WANT="$(grep -i '^ *x-agent-sha256:' "$TMP/headers" | tail -1 | awk '{print $2}' | tr -d '\r' | tr 'A-F' 'a-f')"
[ -n "$WANT" ] || die "the server did not send a checksum for the agent"
GOT="$(sha256sum "$TMP/agent" | awk '{print $1}')"
[ "$GOT" = "$WANT" ] || die "checksum mismatch: expected $WANT, got $GOT"
chmod 0755 "$TMP/agent"
"$TMP/agent" -h >/dev/null 2>&1 || [ $? -eq 2 ] || die "the downloaded file does not run on this machine"
ok "Agent verified (sha256 $GOT)"

if systemctl list-unit-files "$SERVICE.service" >/dev/null 2>&1 && systemctl is-active --quiet "$SERVICE"; then
  info "Stopping the running agent for the upgrade"
  systemctl stop "$SERVICE" || true
fi

install -d -m 0755 "$(dirname "$BIN")"
install -d -m 0700 "$CONF_DIR"
install -m 0755 "$TMP/agent" "$BIN"
ok "Installed $BIN"

if [ "$ENROLLED" -eq 0 ]; then
  info "Enrolling this device with $SERVER"
  ENROLL_ARGS=(-server "$SERVER" -enroll "$TOKEN" -enroll-only -creds "$CREDS")
  [ -n "$SITE" ] && ENROLL_ARGS+=(-site "$SITE")
  "$BIN" "${ENROLL_ARGS[@]}" || die "enrollment failed; the token may have been used already"
  chmod 0600 "$CREDS"
  ok "Device enrolled"
else
  ok "Already enrolled; keeping $CREDS"
fi

"$BIN" -server "$SERVER" -creds "$CREDS" -service install >/dev/null
systemctl daemon-reload
systemctl enable "$SERVICE" >/dev/null 2>&1
systemctl restart "$SERVICE"

for _ in 1 2 3 4 5; do
  systemctl is-active --quiet "$SERVICE" && break
  sleep 1
done
if systemctl is-active --quiet "$SERVICE"; then
  ok "endpoint-agent is running. The device appears online in the console within a few seconds."
else
  journalctl -u "$SERVICE" -n 20 --no-pager || true
  die "endpoint-agent did not start; see the log above"
fi
