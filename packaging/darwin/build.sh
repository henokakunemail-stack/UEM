#!/usr/bin/env bash
# ==============================================================================
# Build script for macOS Agent Package (EndpointAgent.pkg)
# Requires macOS host with pkgbuild and productbuild (Xcode Command Line Tools)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Same single source of truth as the other packaging paths. The installed pkg
# also stamps its payload binary, so darwin gets the same -X treatment.
VERSION="${VERSION:-$(tr -d '[:space:]' < "$REPO_ROOT/VERSION")}"
IDENTIFIER="com.endpoint-mgmt.agent"
PKG_OUTPUT="$SCRIPT_DIR/EndpointAgent-${VERSION}.pkg"

echo "=== Building Endpoint Management Agent macOS Package ==="

# Check environment
if ! command -v pkgbuild >/dev/null 2>&1; then
    echo "[-] pkgbuild not found. This script requires macOS host with Xcode Command Line Tools." >&2
    echo "    To build on macOS, run: xcode-select --install" >&2
    exit 1
fi

PAYLOAD_DIR="$SCRIPT_DIR/payload"
DISTRIBUTION_TMP="$(mktemp)"
cleanup() { rm -rf "$PAYLOAD_DIR" "$DISTRIBUTION_TMP" "$COMPONENT_PKG" "$SCRIPT_DIR/agent-darwin-arm64" "$SCRIPT_DIR/agent-darwin-amd64"; }
trap cleanup EXIT
rm -rf "$PAYLOAD_DIR"
mkdir -p "$PAYLOAD_DIR/usr/local/bin"

# 1. Compile Universal Binary (Apple Silicon + Intel) if on macOS, or specific arch
cd "$REPO_ROOT"
echo "[1/3] Compiling Go binaries for macOS (ARM64 & AMD64)..."
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$VERSION" -o "$SCRIPT_DIR/agent-darwin-arm64" ./agent/cmd/agent
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$VERSION" -o "$SCRIPT_DIR/agent-darwin-amd64" ./agent/cmd/agent

if command -v lipo >/dev/null 2>&1; then
    echo "      Creating universal binary with lipo..."
    lipo -create -output "$PAYLOAD_DIR/usr/local/bin/endpoint-agent" \
        "$SCRIPT_DIR/agent-darwin-arm64" "$SCRIPT_DIR/agent-darwin-amd64"
else
    echo "      lipo not found, defaulting to host architecture..."
    cp "$SCRIPT_DIR/agent-darwin-arm64" "$PAYLOAD_DIR/usr/local/bin/endpoint-agent"
fi
chmod 0755 "$PAYLOAD_DIR/usr/local/bin/endpoint-agent"

# 2. Build Component Package
COMPONENT_PKG="$SCRIPT_DIR/endpoint-agent-component.pkg"
echo "[2/3] Building component package with pkgbuild..."
pkgbuild \
    --root "$PAYLOAD_DIR" \
    --scripts "$SCRIPT_DIR/pkg/scripts" \
    --identifier "$IDENTIFIER" \
    --version "$VERSION" \
    --install-location "/" \
    "$COMPONENT_PKG"

# distribution.xml still carries its tracked placeholder version: the build
# renders the real one rather than editing a tracked file per release.
sed "s/version=\"[^\"]*\"/version=\"$VERSION\"/" "$SCRIPT_DIR/pkg/distribution.xml" > "$DISTRIBUTION_TMP"

# 3. Build Final Distribution Package with GUI Installer
echo "[3/3] Building distribution installer with productbuild..."
productbuild \
    --distribution "$DISTRIBUTION_TMP" \
    --package-path "$SCRIPT_DIR" \
    "$PKG_OUTPUT"

echo "[+] macOS GUI Installer created successfully: $PKG_OUTPUT"
