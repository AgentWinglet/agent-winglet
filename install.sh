#!/usr/bin/env bash
# Installs (or updates — see below) agent-winglet: the Session Ledger hook,
# the desktop dashboard app, or both (the default).
#
# Usage:
#   ./install.sh                 # install/update the hook (global) + the app
#   ./install.sh --hook-only     # just the hook
#   ./install.sh --app-only      # just the app
#   ./install.sh --local         # hook into ./.claude/settings.json instead
#                                 # of ~/.claude/settings.json (--hook-only
#                                 # scope only; the app has no such concept)
#
# When run from a clone of this repo, the hook and app are both built from
# this checkout so their stats schema stays in lockstep. If the script is run
# outside a checkout and only the hook is requested, the hook falls back to
# `go install ...@latest`.
#
# The app is a Wails desktop app whose frontend build output isn't checked
# into git (cmd/agent-winglet-app/frontend/dist is gitignored — it's
# generated), so installing/updating the app always builds from *this
# checkout's* current source — run it from a clone of this repo, and `git
# pull` first if you want the latest app code.
#
# Migration note: if a project already has a per-project hook install from
# before, remove its `ledger-hook` entry from that project's
# `.claude/settings.json` after installing globally — running both at once
# fires the hook twice per event for that project and will double-count
# stats and corrupt the ledger's turn tracking.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib.sh
source "${SCRIPT_DIR}/scripts/lib.sh"

REPO_URL="github.com/umitkaanusta/agent-winglet"
BINARY_NAME="ledger-hook"

WANT_HOOK=1
WANT_APP=1
SETTINGS_FILE="${HOME}/.claude/settings.json"
SCOPE_DESC="globally (~/.claude/settings.json)"

for arg in "$@"; do
  case "$arg" in
    --local)
      SETTINGS_FILE=".claude/settings.json"
      SCOPE_DESC="for this project only (./.claude/settings.json)"
      ;;
    --hook-only) WANT_APP=0 ;;
    --app-only) WANT_HOOK=0 ;;
    *)
      echo "error: unknown argument '${arg}'" >&2
      exit 1
      ;;
  esac
done

if [ "$WANT_HOOK" = "0" ] && [ "$WANT_APP" = "0" ]; then
  echo "error: --hook-only and --app-only are mutually exclusive" >&2
  exit 1
fi

if ! command -v go >/dev/null 2>&1; then
  echo "error: go is not installed. Install Go first: https://go.dev/dl/" >&2
  exit 1
fi

##############################################################################
# Hook
##############################################################################
if [ "$WANT_HOOK" = "1" ]; then
  if ! command -v jq >/dev/null 2>&1; then
    echo "error: jq is required to merge hook config. Install it first (e.g. brew install jq)." >&2
    exit 1
  fi

  # agent-winglet's repo is private, so `go install` can't resolve it through
  # the public module proxy/sumdb (proxy.golang.org has no access to it) —
  # GOPRIVATE tells the go tool to skip both and fetch straight from git
  # instead, using whatever git credentials are already configured for
  # github.com (SSH key, or an HTTPS credential helper set up by e.g.
  # `gh auth login`). This only takes effect for this repo's module path, and
  # is additive to any GOPRIVATE the caller already had set.
  export GOPRIVATE="${GOPRIVATE:+${GOPRIVATE},}${REPO_URL}"

  HOOK_INSTALL_TARGET="${REPO_URL}/cmd/${BINARY_NAME}@latest"
  if [ -f "go.mod" ] && [ -d "cmd/${BINARY_NAME}" ] && grep -q "^module ${REPO_URL}$" go.mod; then
    HOOK_INSTALL_TARGET="./cmd/${BINARY_NAME}"
  fi

  echo "Installing/updating ${BINARY_NAME} ${SCOPE_DESC} from ${HOOK_INSTALL_TARGET}..."
  if ! go install "${HOOK_INSTALL_TARGET}"; then
    echo "error: go install failed — since this repo is private, this is usually" >&2
    echo "a git auth problem rather than a go problem. Make sure git can fetch" >&2
    echo "${REPO_URL} (e.g. 'gh auth setup-git' for HTTPS, or an SSH key added" >&2
    echo "to your GitHub account for the git@ form), then re-run this script." >&2
    exit 1
  fi

  GOBIN="$(go env GOBIN)"
  if [ -z "$GOBIN" ]; then
    GOBIN="$(go env GOPATH)/bin"
  fi
  HOOK_PATH="${GOBIN}/${BINARY_NAME}"

  if [ ! -x "$HOOK_PATH" ]; then
    echo "error: expected binary at ${HOOK_PATH} after go install, but it's not there." >&2
    echo "Make sure ${GOBIN} is on your PATH." >&2
    exit 1
  fi

  mkdir -p "$(dirname "$SETTINGS_FILE")"
  if [ ! -f "$SETTINGS_FILE" ]; then
    echo '{}' > "$SETTINGS_FILE"
  fi

  TMP_FILE="$(mktemp)"
  jq --arg cmd "$HOOK_PATH" '
    def has_cmd: any(.hooks[]?; .command == $cmd);
    .hooks //= {} |
    .hooks.PostToolUse //= [] |
    .hooks.PostToolUse |= (if any(.[]; has_cmd) then . else . + [{"hooks": [{"type": "command", "command": $cmd}]}] end) |
    .hooks.SessionStart //= [] |
    .hooks.SessionStart |= (if any(.[]; has_cmd) then . else . + [{"hooks": [{"type": "command", "command": $cmd}]}] end) |
    .hooks.PostCompact //= [] |
    .hooks.PostCompact |= (if any(.[]; has_cmd) then . else . + [{"hooks": [{"type": "command", "command": $cmd}]}] end) |
    .hooks.SessionEnd //= [] |
    .hooks.SessionEnd |= (if any(.[]; has_cmd) then . else . + [{"hooks": [{"type": "command", "command": $cmd}]}] end)
  ' "$SETTINGS_FILE" > "$TMP_FILE"
  mv "$TMP_FILE" "$SETTINGS_FILE"

  # Unlike before, this script no longer registers a project in
  # ~/.agent-winglet/projects.json itself: with a global install there's no
  # single project directory to register at install time. Instead, the hook
  # binary registers whatever project it's running in the first time it fires
  # there (see cmd/ledger-hook's SessionStart/PostCompact handling and
  # internal/registry.Register) — the desktop app's Projects screen fills in
  # on its own as you use Claude Code in each project.

  echo "Installed/updated. Hook binary: ${HOOK_PATH}"
  echo "Updated: ${SETTINGS_FILE}"
fi

##############################################################################
# App
##############################################################################
if [ "$WANT_APP" = "1" ]; then
  if [ ! -f "go.mod" ] || [ ! -d "cmd/agent-winglet-app" ]; then
    echo "error: --app-only (or the default, which includes the app) must be run from" >&2
    echo "the root of a clone of ${REPO_URL} — the app's frontend build output isn't" >&2
    echo "checked into git, so it's always built from your local checkout, not fetched" >&2
    echo "the way the hook is. Re-run this script from inside that checkout." >&2
    exit 1
  fi

  OS="$(detect_os)"
  if [ "$OS" = "unknown" ]; then
    echo "error: don't know how to install the app on '$(uname -s)'. Supported: macOS, Linux, Windows (via Git Bash/MSYS2)." >&2
    exit 1
  fi

  if ! command -v npm >/dev/null 2>&1; then
    echo "error: npm is required to build the app's frontend. Install Node.js first: https://nodejs.org/" >&2
    exit 1
  fi

  if ! command -v wails >/dev/null 2>&1; then
    WAILS_VERSION="$(grep -oE 'wailsapp/wails/v2 v[0-9]+\.[0-9]+\.[0-9]+' go.mod | awk '{print $2}')"
    WAILS_VERSION="${WAILS_VERSION:-latest}"
    echo "Installing wails CLI (${WAILS_VERSION}, matching go.mod)..."
    go install "github.com/wailsapp/wails/v2/cmd/wails@${WAILS_VERSION}"
    WAILS_GOBIN="$(go env GOBIN)"
    if [ -z "$WAILS_GOBIN" ]; then
      WAILS_GOBIN="$(go env GOPATH)/bin"
    fi
    if [ -x "${WAILS_GOBIN}/wails" ]; then
      export PATH="${WAILS_GOBIN}:$PATH"
    elif [ -x "${WAILS_GOBIN}/wails.exe" ]; then
      export PATH="${WAILS_GOBIN}:$PATH"
    fi
    if ! command -v wails >/dev/null 2>&1; then
      echo "error: installed wails CLI to ${WAILS_GOBIN} but it's not on your PATH. Add ${WAILS_GOBIN} to PATH and re-run." >&2
      exit 1
    fi
  fi

  echo "Building the app for ${OS} (from the current checkout — git pull first for the latest code)..."
  if ! make app; then
    if [ "$OS" = "linux" ]; then
      echo "error: app build failed. On Linux this is usually missing system packages:" >&2
      echo "  sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev" >&2
      echo "(package names vary by distro — see .github/workflows/app-build.yml for the Ubuntu ones CI uses)." >&2
    fi
    exit 1
  fi

  ARTIFACT="cmd/agent-winglet-app/build/bin/$(app_build_artifact "$OS")"
  if [ ! -e "$ARTIFACT" ]; then
    echo "error: expected build output at ${ARTIFACT} but it's not there." >&2
    exit 1
  fi

  DEST="$(app_install_path "$OS")"

  case "$OS" in
    darwin)
      if pgrep -f "/Contents/MacOS/${APP_NAME}" >/dev/null 2>&1; then
        echo "note: ${APP_NAME} is currently running — quit and relaunch it after this finishes to pick up the update."
      fi
      rm -rf "$DEST"
      mkdir -p "$(dirname "$DEST")"
      cp -R "$ARTIFACT" "$DEST"
      # Also clear out the legacy ~/Applications location if a previous
      # install (or a manual `make app` + drag) put it there, so there's
      # only ever one copy of the app on disk.
      ALT="$(app_install_path_alt "$OS")"
      [ -e "$ALT" ] && rm -rf "$ALT"
      echo "Installed/updated app: ${DEST}"
      ;;
    linux)
      mkdir -p "$(dirname "$DEST")" "${HOME}/.local/share/applications" "${HOME}/.local/share/${APP_NAME}"
      cp "$ARTIFACT" "$DEST"
      chmod +x "$DEST"
      ICON="${HOME}/.local/share/${APP_NAME}/appicon.png"
      cp "cmd/agent-winglet-app/build/appicon.png" "$ICON" 2>/dev/null || true
      cat > "${HOME}/.local/share/applications/${APP_NAME}.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=Agent Winglet
Comment=Local-only dashboard for agent-winglet's Claude Code hooks
Exec=${DEST}
Icon=${ICON}
Categories=Utility;
EOF
      command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "${HOME}/.local/share/applications" || true
      echo "Installed/updated app: ${DEST} (also in your app launcher as 'Agent Winglet')"
      case ":$PATH:" in
        *":${HOME}/.local/bin:"*) ;;
        *) echo "note: ${HOME}/.local/bin isn't on your PATH — add it to launch '${APP_NAME}' from a terminal." ;;
      esac
      ;;
    windows)
      mkdir -p "$(dirname "$DEST")"
      cp "$ARTIFACT" "$DEST"
      windows_create_shortcut "$DEST"
      echo "Installed/updated app: ${DEST}"
      ;;
  esac
fi

echo ""
echo "Done. Re-run this script any time to update (git pull first for the latest app code)."
