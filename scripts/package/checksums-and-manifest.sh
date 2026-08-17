#!/usr/bin/env bash
# Generates dist/manifest.json (SPEC.md's Download Page Contract — what
# agentwinglet.com's download page reads to render current downloads
# without hardcoding filenames) and a combined dist/SHA256SUMS, from
# whatever platform artifacts already exist under
# dist/{macos,windows,ubuntu}/. Run this after package-macos/
# package-windows/package-ubuntu have produced their DMG/exe/deb — this
# script doesn't build anything itself, just collects what's already there.
# A missing platform's artifact becomes a null in the manifest rather than
# an error, so this also works to preview the manifest for whatever subset
# of platforms has been built so far (e.g. locally, macOS-only).
#
# Usage: scripts/package/checksums-and-manifest.sh [version]
#   version defaults to scripts/package-lib.sh's resolve_version.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
# shellcheck source=scripts/package-lib.sh
source scripts/package-lib.sh

VERSION="${1:-$(resolve_version)}"
DIST_DIR="dist"

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

# ls with a glob that matches nothing prints an error to stderr and exits
# nonzero under `set -e` — the 2>/dev/null + `|| true` treats "no artifact
# for this platform yet" as a normal, expected state, not a failure.
macos_artifact="$(ls "${DIST_DIR}"/macos/Winglet-*-macOS-universal.dmg 2>/dev/null | head -1 || true)"
windows_artifact="$(ls "${DIST_DIR}"/windows/Winglet-*-windows-x64-setup.exe 2>/dev/null | head -1 || true)"
ubuntu_artifact="$(ls "${DIST_DIR}"/ubuntu/winglet_*_amd64.deb 2>/dev/null | head -1 || true)"

if [ -z "$macos_artifact" ] && [ -z "$windows_artifact" ] && [ -z "$ubuntu_artifact" ]; then
  echo "error: no artifacts found under ${DIST_DIR}/{macos,windows,ubuntu}/. Run make package-macos/package-windows/package-ubuntu first." >&2
  exit 1
fi

sums_file="${DIST_DIR}/SHA256SUMS"
: > "$sums_file"
for f in "$macos_artifact" "$windows_artifact" "$ubuntu_artifact"; do
  [ -n "$f" ] && echo "$(sha256_of "$f")  $(basename "$f")" >> "$sums_file"
done
echo "Wrote ${sums_file}"

released_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

json_download() {
  artifact_path="$1"; label="$2"
  if [ -z "$artifact_path" ]; then
    echo "null"
    return
  fi
  printf '{"label":"%s","artifact":"%s","sha256":"%s"}' \
    "$label" "$(basename "$artifact_path")" "$(sha256_of "$artifact_path")"
}

manifest_path="${DIST_DIR}/manifest.json"
{
  echo "{"
  echo "  \"version\": \"${VERSION}\","
  echo "  \"released_at\": \"${released_at}\","
  echo "  \"downloads\": {"
  echo "    \"macos\": $(json_download "$macos_artifact" "Download for macOS"),"
  echo "    \"windows\": $(json_download "$windows_artifact" "Download for Windows"),"
  echo "    \"ubuntu\": $(json_download "$ubuntu_artifact" "Download for Ubuntu")"
  echo "  }"
  echo "}"
} > "$manifest_path"

if command -v jq >/dev/null 2>&1; then
  jq . "$manifest_path" > "${manifest_path}.tmp" && mv "${manifest_path}.tmp" "$manifest_path"
fi

echo "Wrote ${manifest_path}"
cat "$manifest_path"
