#!/usr/bin/env bash
# ==============================================================================
# Build script for macOS Agent Package (EndpointAgent.pkg)
# Requires macOS host with pkgbuild and productbuild (Xcode Command Line Tools)
# ==============================================================================
#
# Environment:
#   VERSION             release version (default: the repository's VERSION file)
#   AGENT_DARWIN_ARM64  prebuilt darwin/arm64 agent; compiled here when unset
#   AGENT_DARWIN_AMD64  prebuilt darwin/amd64 agent; compiled here when unset
#   PKG_OUTPUT          where to write the package
#                       (default: packaging/darwin/EndpointAgent-<version>.pkg)
#
# The release job passes the agents it already built and stamped, so the
# package carries byte-for-byte the binaries the release publishes and the
# server bundles, rather than a second build of the same source.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Same single source of truth as the other packaging paths. The installed pkg
# also stamps its payload binary, so darwin gets the same -X treatment.
VERSION="${VERSION:-$(tr -d '[:space:]' < "$REPO_ROOT/VERSION")}"
VERSION="${VERSION#v}"
IDENTIFIER="com.endpoint-mgmt.agent"
PKG_OUTPUT="${PKG_OUTPUT:-$SCRIPT_DIR/EndpointAgent-${VERSION}.pkg}"

echo "=== Building Endpoint Management Agent macOS Package ==="

# Check environment
if ! command -v pkgbuild >/dev/null 2>&1; then
    echo "[-] pkgbuild not found. This script requires macOS host with Xcode Command Line Tools." >&2
    echo "    To build on macOS, run: xcode-select --install" >&2
    exit 1
fi

WORK_DIR="$(mktemp -d)"
PAYLOAD_DIR="$WORK_DIR/payload"
SCRIPTS_DIR="$WORK_DIR/scripts"
COMPONENT_DIR="$WORK_DIR/components"
COMPONENT_PKG="$COMPONENT_DIR/endpoint-agent-component.pkg"
DISTRIBUTION_TMP="$WORK_DIR/distribution.xml"
cleanup() { rm -rf "$WORK_DIR"; }
trap cleanup EXIT
mkdir -p "$PAYLOAD_DIR/usr/local/bin" "$SCRIPTS_DIR" "$COMPONENT_DIR"

# 1. Agent binaries (Apple Silicon + Intel)
cd "$REPO_ROOT"
LDFLAGS="-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$VERSION"
ARM64_BIN="${AGENT_DARWIN_ARM64:-}"
AMD64_BIN="${AGENT_DARWIN_AMD64:-}"
if [ -z "$ARM64_BIN" ] || [ -z "$AMD64_BIN" ]; then
    echo "[1/3] Compiling Go binaries for macOS (ARM64 & AMD64)..."
    ARM64_BIN="$WORK_DIR/agent-darwin-arm64"
    AMD64_BIN="$WORK_DIR/agent-darwin-amd64"
    CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -ldflags="$LDFLAGS" -o "$ARM64_BIN" ./agent/cmd/agent
    CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="$LDFLAGS" -o "$AMD64_BIN" ./agent/cmd/agent
else
    echo "[1/3] Using prebuilt agents: $ARM64_BIN, $AMD64_BIN"
fi

if command -v lipo >/dev/null 2>&1; then
    echo "      Creating universal binary with lipo..."
    lipo -create -output "$PAYLOAD_DIR/usr/local/bin/endpoint-agent" "$ARM64_BIN" "$AMD64_BIN"
else
    echo "      lipo not found, defaulting to host architecture..."
    cp "$ARM64_BIN" "$PAYLOAD_DIR/usr/local/bin/endpoint-agent"
fi
chmod 0755 "$PAYLOAD_DIR/usr/local/bin/endpoint-agent"
# The token-gated uninstaller ships with the agent, so removing it never
# depends on finding the release page again.
cp "$SCRIPT_DIR/scripts/uninstall-agent.sh" "$PAYLOAD_DIR/usr/local/bin/endpoint-agent-uninstall"
chmod 0755 "$PAYLOAD_DIR/usr/local/bin/endpoint-agent-uninstall"

# Installer runs only executable scripts, and a Windows checkout or a CI
# checkout of a 100644 file does not carry the bit: copy and set it here.
cp "$SCRIPT_DIR/pkg/scripts/preinstall" "$SCRIPT_DIR/pkg/scripts/postinstall" "$SCRIPTS_DIR/"
chmod 0755 "$SCRIPTS_DIR/preinstall" "$SCRIPTS_DIR/postinstall"

# 2. Build Component Package
echo "[2/3] Building component package with pkgbuild..."
pkgbuild \
    --root "$PAYLOAD_DIR" \
    --scripts "$SCRIPTS_DIR" \
    --identifier "$IDENTIFIER" \
    --version "$VERSION" \
    --install-location "/" \
    "$COMPONENT_PKG"

# distribution.xml still carries its tracked placeholder version: the build
# renders the real one rather than editing a tracked file per release. Only the
# pkg-ref's version is rewritten; a bare version= also matched the XML
# declaration and turned it into <?xml version="1.6.0"?>, which productbuild
# rejects.
sed "s/\(<pkg-ref [^>]*\)version=\"[^\"]*\"/\1version=\"$VERSION\"/" "$SCRIPT_DIR/pkg/distribution.xml" > "$DISTRIBUTION_TMP"

# 3. Build Final Distribution Package with GUI Installer
echo "[3/3] Building distribution installer with productbuild..."
productbuild \
    --distribution "$DISTRIBUTION_TMP" \
    --package-path "$COMPONENT_DIR" \
    "$PKG_OUTPUT"

echo "[+] macOS GUI Installer created successfully: $PKG_OUTPUT"
