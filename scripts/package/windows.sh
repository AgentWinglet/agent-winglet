#!/usr/bin/env bash
# Builds the public Windows deliverable: Winglet.exe + agent-winglet-tray.exe
# bundled into an NSIS installer (signed if credentials are provided, see
# below), dist/windows/Winglet-${VERSION}-windows-x64-setup.exe.
#
# Must run on Windows, under a bash capable of calling native Windows tools
# (Git Bash/MSYS2/Cygwin — same environments install.sh/scripts/lib.sh
# already support): wails build's windows target, makensis, and signtool
# all need a real Windows toolchain.
#
# Requires on PATH:
#   makensis   (ships with NSIS: https://nsis.sourceforge.io/)
#   signtool(.exe) (Windows SDK) — only needed unless UNSIGNED=1
#
# Signing (Authenticode) — set both to sign:
#   WINDOWS_SIGN_PFX_PATH        path to the code-signing .pfx
#   WINDOWS_SIGN_PFX_PASSWORD    its password
#   WINDOWS_SIGN_TIMESTAMP_URL   optional, defaults to DigiCert's RFC3161 server
#
# Set UNSIGNED=1 to skip signing entirely and build an unsigned installer.
# Unsigned binaries trigger a Windows SmartScreen "unrecognized publisher"
# warning for every user (click More info -> Run anyway to get past it) —
# accepted as the current tradeoff until Windows code signing is set up (no
# cert yet; see release.yml's windows-package job, which builds unsigned
# automatically whenever WINDOWS_CERTIFICATE_BASE64 isn't configured).
#
# Usage: scripts/package/windows.sh [version]
#   version defaults to scripts/package-lib.sh's resolve_version.
set -euo pipefail

case "$(uname -s)" in
  MINGW*|MSYS*|CYGWIN*) ;;
  *)
    echo "error: Windows packaging must run on Windows (Git Bash/MSYS2/Cygwin)." >&2
    exit 1
    ;;
esac

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
# shellcheck source=scripts/package-lib.sh
source scripts/package-lib.sh

VERSION="${1:-$(resolve_version)}"
DIST_DIR="dist/windows"
LDFLAGS="-X github.com/umitkaanusta/agent-winglet/internal/buildinfo.Version=${VERSION}"
UNSIGNED="${UNSIGNED:-0}"
TIMESTAMP_URL="${WINDOWS_SIGN_TIMESTAMP_URL:-http://timestamp.digicert.com}"

windows_file_version() {
  local version_base="${1%%[-+]*}"

  if [[ "$version_base" =~ ^[0-9]+[.][0-9]+[.][0-9]+$ ]]; then
    printf '%s.0\n' "$version_base"
  elif [[ "$version_base" =~ ^[0-9]+[.][0-9]+[.][0-9]+[.][0-9]+$ ]]; then
    printf '%s\n' "$version_base"
  else
    echo "error: invalid Windows file version source '${1}': expected a numeric version like 1.2.3, optionally with semver prerelease/build metadata." >&2
    return 1
  fi
}

WINDOWS_FILE_VERSION="$(windows_file_version "$VERSION")"

SIGNTOOL=""
if command -v signtool.exe >/dev/null 2>&1; then
  SIGNTOOL="signtool.exe"
elif command -v signtool >/dev/null 2>&1; then
  SIGNTOOL="signtool"
fi
MAKENSIS=""
if command -v makensis.exe >/dev/null 2>&1; then
  MAKENSIS="makensis.exe"
elif command -v makensis >/dev/null 2>&1; then
  MAKENSIS="makensis"
fi

if [ "$UNSIGNED" != "1" ]; then
  if [ -z "${WINDOWS_SIGN_PFX_PATH:-}" ] || [ -z "${WINDOWS_SIGN_PFX_PASSWORD:-}" ]; then
    echo "error: WINDOWS_SIGN_PFX_PATH and WINDOWS_SIGN_PFX_PASSWORD are required. Set UNSIGNED=1 for an unsigned dev build." >&2
    exit 1
  fi
  if [ -z "$SIGNTOOL" ]; then
    echo "error: signtool(.exe) not found on PATH. Install the Windows SDK." >&2
    exit 1
  fi
fi
if [ -z "$MAKENSIS" ]; then
  echo "error: makensis not found on PATH. Install NSIS: https://nsis.sourceforge.io/" >&2
  exit 1
fi

echo "Packaging Winglet ${VERSION} for Windows (amd64$( [ "$UNSIGNED" = "1" ] && echo ", unsigned" || echo ", signed" ))..."

# NSIS/signtool are native Windows tools — a POSIX-style Git Bash path
# (/c/foo/bar.exe) passed through a -Dname=value string or a signtool
# argument isn't reliably translated the way a bare path argument would be,
# so every path handed to either has to be in native C:\... form first.
win_path() {
  if command -v cygpath >/dev/null 2>&1; then
    cygpath -w "$1"
  else
    printf '%s\n' "$1"
  fi
}

sign() {
  [ "$UNSIGNED" = "1" ] && return 0
  "$SIGNTOOL" sign /f "$(win_path "$WINDOWS_SIGN_PFX_PATH")" /p "$WINDOWS_SIGN_PFX_PASSWORD" \
    /fd sha256 /tr "$TIMESTAMP_URL" /td sha256 "$(win_path "$1")"
}

build_app() {
  (
    cd cmd/agent-winglet-app
    wails build -platform windows/amd64 -ldflags "$LDFLAGS" -clean
  )
}

build_tray() {
  GOOS=windows GOARCH=amd64 go build -ldflags "$LDFLAGS" -o cmd/agent-winglet-tray/build/bin/agent-winglet-tray.exe ./cmd/agent-winglet-tray
}

with_wails_version "$VERSION" build_app
build_tray

app_exe="${REPO_ROOT}/cmd/agent-winglet-app/build/bin/Winglet.exe"
tray_exe="${REPO_ROOT}/cmd/agent-winglet-tray/build/bin/agent-winglet-tray.exe"
sign "$app_exe"
sign "$tray_exe"

mkdir -p "$DIST_DIR"

# wails_tools.nsh's wails.webview2runtime macro (see project.nsi's
# !insertmacro) embeds this small bootstrapper .exe into the installer,
# which downloads the full WebView2 runtime at install time. `wails build
# --nsis` would normally fetch this itself as part of its own NSIS prep,
# but build_app above deliberately skips --nsis (this script calls makensis
# directly instead, per project.nsi's own documented manual-invocation flow,
# so it can pass version overrides that don't go through wails.json) — so
# nothing else fetches it. Not committed to git since it's a binary download,
# not project source.
webview2_bootstrapper="cmd/agent-winglet-app/build/windows/installer/tmp/MicrosoftEdgeWebview2Setup.exe"
mkdir -p "$(dirname "$webview2_bootstrapper")"
curl -fsSL -o "$webview2_bootstrapper" "https://go.microsoft.com/fwlink/p/?LinkId=2124703"

# project.nsi's OutFile and MUI_ICON paths are relative to the .nsi file's
# own directory (per its own header comment's example invocation), so this
# runs makensis from inside build/windows/installer rather than from the
# repo root.
installer_out="cmd/agent-winglet-app/build/bin/Winglet-amd64-installer.exe"
rm -f "$installer_out"
(
  cd cmd/agent-winglet-app/build/windows/installer
  "$MAKENSIS" \
    "-DARG_WAILS_AMD64_BINARY=$(win_path "$app_exe")" \
    "-DARG_TRAY_AMD64_BINARY=$(win_path "$tray_exe")" \
    "-DINFO_PRODUCTVERSION=$VERSION" \
    "-DINFO_VERSIONINFO_VERSION=$WINDOWS_FILE_VERSION" \
    project.nsi
)

installer_dest="${DIST_DIR}/Winglet-${VERSION}-windows-x64-setup.exe"
mv "$installer_out" "$installer_dest"
sign "$installer_dest"

if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "$installer_dest" > "${installer_dest}.sha256"
else
  shasum -a 256 "$installer_dest" > "${installer_dest}.sha256"
fi
echo "Created: ${installer_dest}"
echo "Checksum: $(cat "${installer_dest}.sha256")"
