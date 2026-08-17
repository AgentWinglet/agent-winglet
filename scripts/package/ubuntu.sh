#!/usr/bin/env bash
# Builds the public Ubuntu deliverable: a .deb bundling Winglet + the tray
# helper, dist/ubuntu/winglet_${VERSION}_amd64.deb — see SPEC.md's Ubuntu
# Package section for the artifact layout and why nothing here writes into
# a user's home directory (that happens on first app launch instead — see
# cmd/agent-winglet-app/autostart_linux.go).
#
# Must run on Linux: wails build's linux target needs GTK/WebKit dev
# headers, and the tray helper needs GTK + an AppIndicator binding (both
# cgo) — see .github/workflows/app-build.yml's "Install Linux system
# dependencies" step for the exact packages this needs installed.
#
# Requires nfpm (https://nfpm.goreleaser.com/) on PATH — installed
# automatically via `go install` if missing, same pattern install.sh uses
# for the wails CLI.
#
# Usage: scripts/package/ubuntu.sh [version]
#   version defaults to scripts/package-lib.sh's resolve_version.
set -euo pipefail

if [ "$(uname -s)" != "Linux" ]; then
  echo "error: Ubuntu packaging must run on Linux." >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
# shellcheck source=scripts/package-lib.sh
source scripts/package-lib.sh

VERSION="${1:-$(resolve_version)}"
DIST_DIR="dist/ubuntu"
LDFLAGS="-X github.com/umitkaanusta/agent-winglet/internal/buildinfo.Version=${VERSION}"
DEBIAN_DIR="scripts/package/debian"

if ! command -v nfpm >/dev/null 2>&1; then
  echo "Installing nfpm..."
  go install github.com/goreleaser/nfpm/v2/cmd/nfpm@latest
  NFPM_GOBIN="$(go env GOBIN)"
  [ -z "$NFPM_GOBIN" ] && NFPM_GOBIN="$(go env GOPATH)/bin"
  export PATH="${NFPM_GOBIN}:$PATH"
  if ! command -v nfpm >/dev/null 2>&1; then
    echo "error: installed nfpm to ${NFPM_GOBIN} but it's not on PATH." >&2
    exit 1
  fi
fi

echo "Packaging Winglet ${VERSION} for Ubuntu (amd64)..."

build_app() {
  (
    cd cmd/agent-winglet-app
    tag=""
    pkg-config --exists webkit2gtk-4.1 2>/dev/null && tag="-tags webkit2_41"
    # shellcheck disable=SC2086
    wails build -platform linux/amd64 -ldflags "$LDFLAGS" $tag -clean
  )
}

build_tray() {
  GOOS=linux GOARCH=amd64 go build -ldflags "$LDFLAGS" -o cmd/agent-winglet-tray/build/bin/agent-winglet-tray ./cmd/agent-winglet-tray
}

with_wails_version "$VERSION" build_app
build_tray

mkdir -p "$DIST_DIR"

# nfpm.yaml.generated, winglet.desktop, and appicon-256.png are staged
# alongside the template/scripts in $DEBIAN_DIR so every path nfpm.yaml
# references resolves the same way regardless of the caller's cwd, then
# cleaned up afterward — none of these three are meant to be committed
# (the real source assets are cmd/agent-winglet-app/build/linux/*, and
# nfpm.yaml.tmpl).
cleanup_staged() {
  rm -f "${DEBIAN_DIR}/nfpm.yaml.generated" "${DEBIAN_DIR}/winglet.desktop" "${DEBIAN_DIR}/appicon-256.png"
}
trap cleanup_staged EXIT

sed \
  -e "s#__VERSION__#${VERSION}#g" \
  -e "s#__APP_BIN__#${REPO_ROOT}/cmd/agent-winglet-app/build/bin/Winglet#g" \
  -e "s#__TRAY_BIN__#${REPO_ROOT}/cmd/agent-winglet-tray/build/bin/agent-winglet-tray#g" \
  "${DEBIAN_DIR}/nfpm.yaml.tmpl" > "${DEBIAN_DIR}/nfpm.yaml.generated"
cp cmd/agent-winglet-app/build/linux/winglet.desktop "${DEBIAN_DIR}/winglet.desktop"
cp cmd/agent-winglet-app/build/linux/appicon-256.png "${DEBIAN_DIR}/appicon-256.png"

deb_out="${DIST_DIR}/winglet_${VERSION}_amd64.deb"
rm -f "$deb_out"
nfpm package --config "${DEBIAN_DIR}/nfpm.yaml.generated" --packager deb --target "$deb_out"

sha256sum "$deb_out" > "${deb_out}.sha256"
echo "Created: ${deb_out}"
echo "Checksum: $(cat "${deb_out}.sha256")"
