# Sourced by the scripts/package/*.sh scripts and the Makefile's package-*
# targets — shared version resolution and the wails.json patch/restore
# helper both need. Not meant to be run directly. Distinct from scripts/lib.sh,
# which is specifically install.sh/uninstall.sh's path-convention library.

WAILS_JSON="cmd/agent-winglet-app/wails.json"

# Prints the version to build/package with: the current commit's exact
# release tag (vMAJOR.MINOR.PATCH, without the leading v) if there is one,
# otherwise a dev-build stand-in so a local `make package-*` run still
# produces an artifact that's obviously not a tagged release.
resolve_version() {
  if git describe --tags --exact-match --match 'v[0-9]*.[0-9]*.[0-9]*' >/dev/null 2>&1; then
    git describe --tags --exact-match --match 'v[0-9]*.[0-9]*.[0-9]*' | sed 's/^v//'
    return
  fi
  sha="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
  dirty=""
  git diff --quiet 2>/dev/null || dirty="-dirty"
  echo "0.0.0-dev+${sha}${dirty}"
}

# Runs "$@" with wails.json's info.productVersion temporarily patched to
# VERSION. wails build (and, through build/windows/info.json and
# build/darwin/Info.plist's {{.Info.ProductVersion}} templates, the NSIS
# installer and the macOS bundle's Info.plist) all read the product version
# from there, and it has to match the artifact filename/download manifest
# for a real release. Restores the original file afterward no matter how the
# command exits — including a build failure, which matters because callers
# run under `set -e`: a RETURN trap does NOT fire when `set -e` aborts the
# script from inside "$@" (the function never "returns" normally, it's torn
# down immediately), so this uses an EXIT trap instead, which fires
# unconditionally. A RETURN-trap version of this bit a real
# `make package-macos` run — a failed wails build left wails.json patched
# on disk afterward.
with_wails_version() {
  version="$1"; shift
  if ! command -v jq >/dev/null 2>&1; then
    echo "error: jq is required to set the release version. Install it first (e.g. brew install jq)." >&2
    return 1
  fi
  backup="$(mktemp)"
  cp "$WAILS_JSON" "$backup"
  trap 'mv "$backup" "$WAILS_JSON"' EXIT
  tmp="$(mktemp)"
  jq --arg v "$version" '.info.productVersion = $v' "$WAILS_JSON" > "$tmp"
  mv "$tmp" "$WAILS_JSON"
  "$@"
  status=$?
  mv "$backup" "$WAILS_JSON"
  trap - EXIT
  return $status
}
