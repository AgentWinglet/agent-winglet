#!/usr/bin/env bash
# Builds the public macOS deliverable: a universal (amd64+arm64) Winglet.app
# with a universal nested tray helper, signed with a Developer ID
# Application certificate, notarized, stapled, and packaged into
# dist/macos/Winglet-${VERSION}-macOS-universal.dmg. Follows Apple's
# recommended order: build -> sign -> package dmg -> sign dmg -> notarize ->
# staple.
#
# Requires:
#   MACOS_SIGN_IDENTITY   Developer ID Application signing identity, e.g.
#                         "Developer ID Application: Jane Doe (ABCDE12345)".
#                         List installed identities with:
#                           security find-identity -v -p codesigning
#                         Auto-detected if exactly one Developer ID
#                         Application identity is installed and this isn't
#                         set.
#
#   Notarization credentials (xcrun notarytool) — one of:
#     MACOS_NOTARY_PROFILE
#         A keychain profile created once via:
#           xcrun notarytool store-credentials <profile-name> \
#             --apple-id you@example.com --team-id TEAMID --password app-specific-password
#         Convenient for local builds on a single Mac; the profile lives in
#         that Mac's keychain, so it doesn't work on CI's ephemeral runners.
#     MACOS_NOTARY_KEY_PATH / MACOS_NOTARY_KEY_ID / MACOS_NOTARY_ISSUER_ID
#         An App Store Connect API key (.p8 file path, key ID, issuer ID).
#         Works anywhere, including CI — see .github/workflows/release.yml,
#         which writes the .p8 from a secret to a temp path per run.
#
# Set UNSIGNED=1 to skip signing/notarization entirely and build an ad-hoc-
# only DMG instead (the old v1-before-a-Developer-ID behavior) — useful for
# quick local iteration without hitting Apple's servers every time. Never
# use an UNSIGNED=1 build for a public release: it will show Gatekeeper's
# "Apple could not verify this app" warning for every user.
#
# Usage: scripts/package/macos.sh [version]
#   version defaults to scripts/package-lib.sh's resolve_version.
set -euo pipefail

if [ "$(uname -s)" != "Darwin" ]; then
  echo "error: macOS packaging must run on macOS." >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$REPO_ROOT"
# shellcheck source=scripts/package-lib.sh
source scripts/package-lib.sh

VERSION="${1:-$(resolve_version)}"
DIST_DIR="dist/macos"
APP_BUNDLE="cmd/agent-winglet-app/build/bin/Winglet.app"
TRAY_BUNDLE="${APP_BUNDLE}/Contents/Library/LoginItems/Tray.app"
ENTITLEMENTS="cmd/agent-winglet-app/build/darwin/entitlements.plist"
LDFLAGS="-X github.com/umitkaanusta/agent-winglet/internal/buildinfo.Version=${VERSION}"
UNSIGNED="${UNSIGNED:-0}"

SIGN_IDENTITY="${MACOS_SIGN_IDENTITY:-}"
if [ "$UNSIGNED" != "1" ] && [ -z "$SIGN_IDENTITY" ]; then
  found="$(security find-identity -v -p codesigning 2>/dev/null | grep -c '"Developer ID Application:' || true)"
  if [ "$found" = "1" ]; then
    SIGN_IDENTITY="$(security find-identity -v -p codesigning | grep '"Developer ID Application:' | sed -E 's/.*"(Developer ID Application:[^"]+)".*/\1/')"
    echo "Auto-detected signing identity: ${SIGN_IDENTITY}"
  fi
fi
if [ "$UNSIGNED" != "1" ] && [ -z "$SIGN_IDENTITY" ]; then
  echo "error: no signing identity. Set MACOS_SIGN_IDENTITY (see this script's header), or set UNSIGNED=1 for an unsigned dev build." >&2
  echo "Installed identities:" >&2
  security find-identity -v -p codesigning >&2 || true
  exit 1
fi
if [ "$UNSIGNED" != "1" ]; then
  if [ -z "${MACOS_NOTARY_PROFILE:-}" ] && [ -z "${MACOS_NOTARY_KEY_PATH:-}" ]; then
    echo "error: no notarization credentials. Set MACOS_NOTARY_PROFILE, or MACOS_NOTARY_KEY_PATH/_KEY_ID/_ISSUER_ID (see this script's header)." >&2
    exit 1
  fi
fi

echo "Packaging Winglet ${VERSION} for macOS (universal$( [ "$UNSIGNED" = "1" ] && echo ", unsigned, unnotarized" || echo ", signed and notarized" ))..."

build_universal_app() {
  # wails build -clean only clears its own known output file, not the whole
  # bundle — a previous run's Contents/Library/LoginItems/Tray.app (added by
  # nest_universal_tray below, after wails build finishes) is left in place.
  # wails build's own post-build self-sign step then chokes on that stale,
  # possibly-incomplete Tray.app from a prior interrupted run. Removing the
  # whole bundle first guarantees every run starts from an actually clean
  # slate.
  rm -rf cmd/agent-winglet-app/build/bin
  (
    cd cmd/agent-winglet-app
    CGO_LDFLAGS="-framework UniformTypeIdentifiers" wails build -platform darwin/universal -ldflags "$LDFLAGS" -clean
  )
}

# Builds the tray helper for both macOS architectures and lipos them into a
# single universal binary nested inside the just-built app bundle — mirrors
# the Makefile's nest-tray-darwin target, but for both architectures instead
# of the host one (a host-arch-only nested tray would make an Apple
# Silicon-built .app appear to support Intel via the outer Wails target
# while the nested login item silently doesn't).
nest_universal_tray() {
  rm -rf "$TRAY_BUNDLE"
  mkdir -p "${TRAY_BUNDLE}/Contents/MacOS" "${TRAY_BUNDLE}/Contents/Resources"

  tmp_amd64="$(mktemp)"
  tmp_arm64="$(mktemp)"
  # CGO_ENABLED must be explicit: Go only defaults it on for the exact
  # native GOOS/GOARCH combo, so cross-arch (e.g. building amd64 from an
  # arm64 host) silently falls back to CGO_ENABLED=0 without this — which
  # drops getlantern/systray's cgo-backed darwin implementation entirely and
  # fails at link time with "undefined: nativeLoop" et al. Xcode's clang
  # handles both architectures natively via -arch, so this cross-compiles
  # cleanly either direction.
  GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$tmp_amd64" ./cmd/agent-winglet-tray
  GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build -ldflags "$LDFLAGS" -o "$tmp_arm64" ./cmd/agent-winglet-tray
  lipo -create -output "${TRAY_BUNDLE}/Contents/MacOS/agent-winglet-tray" "$tmp_amd64" "$tmp_arm64"
  rm -f "$tmp_amd64" "$tmp_arm64"

  cp cmd/agent-winglet-tray/build/darwin/Info.plist "${TRAY_BUNDLE}/Contents/Info.plist"
  cp cmd/agent-winglet-tray/build/darwin/appicon.icns "${TRAY_BUNDLE}/Contents/Resources/appicon.icns"
}

# Ad-hoc-signs the whole bundle — just enough for it to execute at all on
# Apple Silicon, not a Developer ID signature, and not sufficient for
# Gatekeeper. Used only for UNSIGNED=1 dev builds; sign_bundle below replaces
# this signature with a real one for a public release.
adhoc_sign_bundle() {
  codesign --sign - --force --deep "$APP_BUNDLE"
}

# Signs from the inside out — nested helper first, then its containing
# Tray.app, then the outer app's own binary, then the outer bundle — each
# with hardened runtime and a secure timestamp (both required for
# notarization). Explicit binary-by-binary signing rather than one
# `--deep` pass on the outer bundle: Apple recommends against --deep for
# anything beyond the most trivial bundles, since it can silently skip or
# mis-sign nested content.
sign_bundle() {
  codesign --sign "$SIGN_IDENTITY" --force --options runtime --timestamp \
    "${TRAY_BUNDLE}/Contents/MacOS/agent-winglet-tray"
  codesign --sign "$SIGN_IDENTITY" --force --options runtime --timestamp \
    "$TRAY_BUNDLE"
  codesign --sign "$SIGN_IDENTITY" --force --options runtime --timestamp \
    --entitlements "$ENTITLEMENTS" \
    "${APP_BUNDLE}/Contents/MacOS/Winglet"
  codesign --sign "$SIGN_IDENTITY" --force --options runtime --timestamp \
    --entitlements "$ENTITLEMENTS" \
    "$APP_BUNDLE"

  codesign --verify --deep --strict --verbose=2 "$APP_BUNDLE"
}

create_dmg() {
  mkdir -p "$DIST_DIR"
  staging="$(mktemp -d)"
  trap 'rm -rf "$staging"' RETURN
  cp -R "$APP_BUNDLE" "${staging}/Winglet.app"
  ln -s /Applications "${staging}/Applications"

  dmg_path="${DIST_DIR}/Winglet-${VERSION}-macOS-universal.dmg"
  rm -f "$dmg_path"
  hdiutil create -volname "Winglet" -srcfolder "$staging" -ov -format UDZO "$dmg_path" -quiet
  echo "$dmg_path"
}

sign_dmg() {
  codesign --sign "$SIGN_IDENTITY" --timestamp "$1"
}

notarize_and_staple() {
  target="$1"
  args=(notarytool submit "$target" --wait)
  if [ -n "${MACOS_NOTARY_PROFILE:-}" ]; then
    args+=(--keychain-profile "$MACOS_NOTARY_PROFILE")
  else
    args+=(--key "$MACOS_NOTARY_KEY_PATH" --key-id "$MACOS_NOTARY_KEY_ID" --issuer "$MACOS_NOTARY_ISSUER_ID")
  fi
  echo "Submitting for notarization (this polls Apple and can take several minutes)..."
  xcrun "${args[@]}"
  xcrun stapler staple "$target"
  xcrun stapler validate "$target"
}

with_wails_version "$VERSION" build_universal_app
nest_universal_tray

if [ "$UNSIGNED" = "1" ]; then
  adhoc_sign_bundle
else
  sign_bundle
fi

dmg_path="$(create_dmg)"

if [ "$UNSIGNED" != "1" ]; then
  sign_dmg "$dmg_path"
  notarize_and_staple "$dmg_path"
  echo "Gatekeeper check:"
  spctl --assess --type open --context context:primary-signature --verbose "$dmg_path"
fi

shasum -a 256 "$dmg_path" > "${dmg_path}.sha256"
echo "Created: ${dmg_path}"
echo "Checksum: $(cat "${dmg_path}.sha256")"
