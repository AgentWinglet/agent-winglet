#!/usr/bin/env bash
# Checks scripts/package/macos.sh's output against SPEC.md's macOS
# Acceptance Criteria: architecture (universal app + tray), and — since
# signing/notarization is wired into the pipeline — a real Developer ID
# signature and a Gatekeeper-passing DMG.
#
# Set UNSIGNED_OK=1 to treat the signing/notarization checks as
# informational instead of required, for verifying an UNSIGNED=1 dev build
# (scripts/package/macos.sh) instead of a real release.
#
# Usage: scripts/package/macos-verify.sh [path/to/Winglet.app] [path/to/Winglet.dmg]
#   app path defaults to cmd/agent-winglet-app/build/bin/Winglet.app
#   dmg path is optional — pass it to also check the packaged DMG
set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "error: macOS verification must run on macOS." >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"

APP_BUNDLE="${1:-cmd/agent-winglet-app/build/bin/Winglet.app}"
DMG_PATH="${2:-}"
TRAY_BIN="${APP_BUNDLE}/Contents/Library/LoginItems/Tray.app/Contents/MacOS/agent-winglet-tray"
APP_BIN="${APP_BUNDLE}/Contents/MacOS/Winglet"
UNSIGNED_OK="${UNSIGNED_OK:-0}"

fail=0
check() {
  desc="$1"; shift
  if "$@"; then
    echo "PASS: ${desc}"
  else
    if [ "$UNSIGNED_OK" = "1" ]; then
      echo "SKIP (UNSIGNED_OK=1): ${desc}"
    else
      echo "FAIL: ${desc}"
      fail=1
    fi
  fi
}

check_arch() {
  file "$1" | grep -q "$2"
}

echo "Verifying ${APP_BUNDLE}..."
check "app binary reports x86_64"  check_arch "$APP_BIN" "x86_64"
check "app binary reports arm64"   check_arch "$APP_BIN" "arm64"
check "tray binary reports x86_64" check_arch "$TRAY_BIN" "x86_64"
check "tray binary reports arm64"  check_arch "$TRAY_BIN" "arm64"

check "codesign --verify (Developer ID signature)" \
  codesign --verify --deep --strict --verbose=2 "$APP_BUNDLE"
check "spctl --assess (Gatekeeper accepts the app)" \
  spctl --assess --type execute --verbose "$APP_BUNDLE"

if [ -n "$DMG_PATH" ]; then
  echo ""
  echo "Verifying ${DMG_PATH}..."
  check "DMG has a stapled notarization ticket" \
    xcrun stapler validate "$DMG_PATH"
  check "spctl --assess (Gatekeeper accepts the DMG, no network needed)" \
    spctl --assess --type open --context context:primary-signature --verbose "$DMG_PATH"
fi

if [ "$fail" != "0" ]; then
  echo ""
  echo "One or more required checks failed." >&2
  exit 1
fi
echo ""
echo "All required checks passed."
