#!/usr/bin/env bash
# ==============================================================================
# Build script for Linux Agent Packages (.deb and GUI bundle)
# ==============================================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# The VERSION file at the repository root is the single source of truth for
# every packaging path. The env override keeps the CI release path workable,
# which numbers installers by run number rather than the release version.
VERSION="${VERSION:-$(tr -d '[:space:]' < "$REPO_ROOT/VERSION")}"
ARCH="${1:-amd64}"
if ! printf '%s' "$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+'; then
    echo "VERSION '$VERSION' does not look like a release version (expected e.g. 1.4.0)" >&2
    exit 1
fi

echo "=== Building Endpoint Management Agent Linux Package ==="
echo "Version: $VERSION, Arch: $ARCH"

# 1. Compile agent binary
cd "$REPO_ROOT"
OUT_BIN="$SCRIPT_DIR/agent-linux-$ARCH"
mkdir -p "$(dirname "$OUT_BIN")"

echo "[1/3] Compiling agent binary for linux/$ARCH..."
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
  go build -ldflags="-s -w -X github.com/henokakunemail-stack/Endpoint-Manager/agent/shared/osinfo.Version=$VERSION" -o "$OUT_BIN" ./agent/cmd/agent

# Also copy to root for general convenience
cp "$OUT_BIN" "$REPO_ROOT/agent-linux-$ARCH"

# The DEBIAN metadata has to be re-rendered per arch and per version, but the
# tracked copy is the template. Sed-ing it in place dirtied `git status` every
# time somebody built for a different arch. Build into a scratch copy instead;
# dpkg-deb packs from there, and the tree stays clean.
PKG_ROOT="$(mktemp -d)"
trap 'rm -rf "$PKG_ROOT"' EXIT
cp -r "$SCRIPT_DIR/deb" "$PKG_ROOT/deb"
sed -i -e "s/^Architecture: .*/Architecture: $ARCH/" \
       -e "s/^Version: .*/Version: $VERSION/" "$PKG_ROOT/deb/DEBIAN/control"
chmod 0755 "$PKG_ROOT/deb/DEBIAN/postinst" "$PKG_ROOT/deb/DEBIAN/prerm" "$PKG_ROOT/deb/DEBIAN/postrm"
mkdir -p "$PKG_ROOT/deb/usr/bin"
cp "$OUT_BIN" "$PKG_ROOT/deb/usr/bin/endpoint-agent"

# 3. Build .deb package if dpkg-deb is available
if command -v dpkg-deb >/dev/null 2>&1; then
    echo "[2/3] Building .deb package..."
    DEB_NAME="$SCRIPT_DIR/endpoint-agent_${VERSION}_${ARCH}.deb"
    dpkg-deb --build --root-owner-group "$PKG_ROOT/deb" "$DEB_NAME"
    echo "[+] Debian package created: $DEB_NAME"
else
    echo "[!] dpkg-deb not found on this host. Skipping .deb packaging."
    echo "    On Ubuntu/Debian, install dpkg-dev to build .deb packages."
fi

# 4. Make GUI installer executable
chmod +x "$SCRIPT_DIR/gui-installer/install-gui.sh" "$SCRIPT_DIR/gui-installer/uninstall-gui.sh"
echo "[3/3] GUI installer scripts ready in packaging/linux/gui-installer/"
