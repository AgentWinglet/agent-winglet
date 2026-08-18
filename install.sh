#!/usr/bin/env bash
# Installs (or updates — see below) agent-winglet: the agent hooks, the
# desktop dashboard app, or both (the default).
#
# Usage:
#   ./install.sh                 # install/update both hooks (global) + the app
#   ./install.sh --hook-only     # just the hooks
#   ./install.sh --app-only      # just the app
#   ./install.sh --claude-only   # install/update only the Claude hook
#   ./install.sh --codex-only    # install/update only the Codex hook
#   ./install.sh --local         # hook into project config files instead
#                                 # of global config files (--hook-only
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
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib.sh
source "${SCRIPT_DIR}/scripts/lib.sh"

REPO_URL="github.com/umitkaanusta/agent-winglet"

WANT_HOOK=1
WANT_APP=1
WANT_CLAUDE_HOOK=1
WANT_CODEX_HOOK=1
CLAUDE_SETTINGS_FILE="${HOME}/.claude/settings.json"
CODEX_HOME_DIR="${CODEX_HOME:-${HOME}/.codex}"
CODEX_HOOKS_FILE="${CODEX_HOME_DIR}/hooks.json"
CODEX_CONFIG_FILE="${CODEX_HOME_DIR}/config.toml"
SCOPE_DESC="globally"
SELECTED_CLAUDE_ONLY=0
SELECTED_CODEX_ONLY=0

for arg in "$@"; do
  case "$arg" in
    --local)
      CLAUDE_SETTINGS_FILE=".claude/settings.json"
      CODEX_HOOKS_FILE=".codex/hooks.json"
      CODEX_CONFIG_FILE=".codex/config.toml"
      SCOPE_DESC="for this project only"
      ;;
    --hook-only) WANT_APP=0 ;;
    --app-only) WANT_HOOK=0 ;;
    --claude-only) WANT_CODEX_HOOK=0; SELECTED_CLAUDE_ONLY=1 ;;
    --codex-only) WANT_CLAUDE_HOOK=0; SELECTED_CODEX_ONLY=1 ;;
    *)
      echo "error: unknown argument '${arg}'" >&2
      exit 1
      ;;
  esac
done

if [ "$SELECTED_CLAUDE_ONLY" = "1" ] && [ "$SELECTED_CODEX_ONLY" = "1" ]; then
  echo "error: --claude-only and --codex-only are mutually exclusive" >&2
  exit 1
fi

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

  GOBIN="$(go env GOBIN)"
  if [ -z "$GOBIN" ]; then
    GOBIN="$(go env GOPATH)/bin"
  fi

  install_hook_binary() {
    binary_name="$1"
    hook_install_target="${REPO_URL}/cmd/${binary_name}@latest"
    if [ -f "go.mod" ] && [ -d "cmd/${binary_name}" ] && grep -q "^module ${REPO_URL}$" go.mod; then
      hook_install_target="./cmd/${binary_name}"
    fi

    echo "Installing/updating ${binary_name} ${SCOPE_DESC} from ${hook_install_target}..." >&2
    if ! go install "${hook_install_target}"; then
      echo "error: go install failed — since this repo is private, this is usually" >&2
      echo "a git auth problem rather than a go problem. Make sure git can fetch" >&2
      echo "${REPO_URL} (e.g. 'gh auth setup-git' for HTTPS, or an SSH key added" >&2
      echo "to your GitHub account for the git@ form), then re-run this script." >&2
      exit 1
    fi

    hook_path="${GOBIN}/${binary_name}"
    if [ ! -x "$hook_path" ]; then
      echo "error: expected binary at ${hook_path} after go install, but it's not there." >&2
      echo "Make sure ${GOBIN} is on your PATH." >&2
      exit 1
    fi
    printf '%s\n' "$hook_path"
  }

  merge_claude_hook() {
    hook_path="$1"
    mkdir -p "$(dirname "$CLAUDE_SETTINGS_FILE")"
    if [ ! -f "$CLAUDE_SETTINGS_FILE" ]; then
      echo '{}' > "$CLAUDE_SETTINGS_FILE"
    fi

    tmp_file="$(mktemp)"
    jq --arg cmd "$hook_path" '
      def has_cmd: any(.hooks[]?; .command == $cmd);
      .hooks //= {} |
      .hooks.PostToolUse //= [] |
      .hooks.PostToolUse |= (if any(.[]; has_cmd) then . else . + [{"hooks": [{"type": "command", "command": $cmd}]}] end) |
      .hooks.SessionStart //= [] |
      .hooks.SessionStart |= (if any(.[]; has_cmd) then . else . + [{"hooks": [{"type": "command", "command": $cmd}]}] end) |
      .hooks.PostCompact //= [] |
      .hooks.PostCompact |= (if any(.[]; has_cmd) then . else . + [{"hooks": [{"type": "command", "command": $cmd}]}] end) |
      .hooks.Stop //= [] |
      .hooks.Stop |= (if any(.[]; has_cmd) then . else . + [{"hooks": [{"type": "command", "command": $cmd}]}] end) |
      .hooks.SessionEnd //= [] |
      .hooks.SessionEnd |= (if any(.[]; has_cmd) then . else . + [{"hooks": [{"type": "command", "command": $cmd}]}] end)
    ' "$CLAUDE_SETTINGS_FILE" > "$tmp_file"
    mv "$tmp_file" "$CLAUDE_SETTINGS_FILE"
  }

  merge_codex_hook() {
    hook_path="$1"
    mkdir -p "$(dirname "$CODEX_HOOKS_FILE")"
    preserve_mtime=0
    mtime_ref=""
    if [ -f "$CODEX_HOOKS_FILE" ] && jq -e '
      [.hooks // {} | .. | objects | select(has("command")) | .command | select((split("/") | last) == "codex-hook")] | length > 0
    ' "$CODEX_HOOKS_FILE" >/dev/null; then
      preserve_mtime=1
      mtime_ref="$(mktemp)"
      touch -r "$CODEX_HOOKS_FILE" "$mtime_ref"
    fi
    if [ ! -f "$CODEX_HOOKS_FILE" ]; then
      echo '{}' > "$CODEX_HOOKS_FILE"
    fi

    tmp_file="$(mktemp)"
    # Codex clamps a SessionEnd hook's timeout to 3s (and logs a "clamping
    # SessionEnd hook timeout" warning if configured any higher), so it gets
    # its own lower ceiling here instead of sharing hook_entry's default 5s.
    jq --arg cmd "$hook_path" '
      def has_cmd: any(.hooks[]?; .command == $cmd);
      def hook_entry(timeout): {"hooks": [{"type": "command", "command": $cmd, "timeout": timeout}]};
      .hooks //= {} |
      .hooks.PostToolUse //= [] |
      .hooks.PostToolUse |= (if any(.[]; has_cmd) then . else . + [hook_entry(5) + {"matcher": ""}] end) |
      .hooks.SessionStart //= [] |
      .hooks.SessionStart |= (if any(.[]; has_cmd) then . else . + [hook_entry(5)] end) |
      .hooks.PostCompact //= [] |
      .hooks.PostCompact |= (if any(.[]; has_cmd) then . else . + [hook_entry(5)] end) |
      .hooks.Stop //= [] |
      .hooks.Stop |= (if any(.[]; has_cmd) then . else . + [hook_entry(5)] end) |
      .hooks.SubagentStart //= [] |
      .hooks.SubagentStart |= (if any(.[]; has_cmd) then . else . + [hook_entry(5)] end) |
      .hooks.SubagentStop //= [] |
      .hooks.SubagentStop |= (if any(.[]; has_cmd) then . else . + [hook_entry(5)] end) |
      .hooks.SessionEnd //= [] |
      .hooks.SessionEnd |= (if any(.[]; has_cmd) then . else . + [hook_entry(3)] end)
    ' "$CODEX_HOOKS_FILE" > "$tmp_file"
    mv "$tmp_file" "$CODEX_HOOKS_FILE"
    if [ "$preserve_mtime" = "1" ]; then
      touch -r "$mtime_ref" "$CODEX_HOOKS_FILE"
      rm -f "$mtime_ref"
    fi
  }

  if [ "$WANT_CLAUDE_HOOK" = "1" ]; then
    CLAUDE_HOOK_PATH="$(install_hook_binary "claude-hook")"
    merge_claude_hook "$CLAUDE_HOOK_PATH"
    echo "Installed/updated Claude hook binary: ${CLAUDE_HOOK_PATH}"
    echo "Updated: ${CLAUDE_SETTINGS_FILE}"
  fi

  if [ "$WANT_CODEX_HOOK" = "1" ]; then
    CODEX_HOOK_PATH="$(install_hook_binary "codex-hook")"
    merge_codex_hook "$CODEX_HOOK_PATH"
    echo "Installed/updated Codex hook binary: ${CODEX_HOOK_PATH}"
    echo "Updated: ${CODEX_HOOKS_FILE}"
    if [ -f "$CODEX_CONFIG_FILE" ] && grep -Eq '^[[:space:]]*hooks[[:space:]]*=[[:space:]]*false' "$CODEX_CONFIG_FILE"; then
      echo "warning: ${CODEX_CONFIG_FILE} appears to set hooks = false; Codex will not run hooks until you re-enable them."
    fi
    echo "In Codex, open Settings > Hooks and trust the agent-winglet codex-hook before Winglet can record Codex sessions."
  fi

  # Unlike before, this script no longer registers a project in
  # ~/.agent-winglet/projects.json itself: with a global install there's no
  # single project directory to register at install time. Instead, each hook
  # binary registers whatever project it's running in the first time it fires
  # there (see cmd/claude-hook and cmd/codex-hook SessionStart handling and
  # internal/registry.Register) — the desktop app's Projects screen fills in
  # on its own as you use Claude Code or Codex in each project.
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
Name=Winglet
Comment=Trims wasted context from your coding agent sessions, so your plan goes further
Exec=${DEST}
Icon=${ICON}
Categories=Utility;
EOF
      command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "${HOME}/.local/share/applications" || true
      echo "Installed/updated app: ${DEST} (also in your app launcher as 'Winglet')"
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

  ############################################################################
  # Tray helper (menu-bar/tray glance icon + login item — see
  # cmd/agent-winglet-tray's doc comment).
  #
  # darwin: `make app` above already built and nested it inside the just-
  # installed Winglet.app (see the Makefile's nest-tray-darwin target) — all
  # that's left is asking macOS to actually register it as a login item,
  # which can only be done by the app itself (see
  # cmd/agent-winglet-app/loginitem_darwin.go), hence shelling out to the
  # binary we just installed rather than doing it from this script directly.
  #
  # linux/windows have no such API, so they keep the plain
  # build-a-separate-binary-and-write-an-autostart-entry approach.
  ############################################################################
  if [ "$OS" = "darwin" ]; then
    stop_tray
    if "$DEST/Contents/MacOS/${APP_NAME}" --register-login-item; then
      echo "Registered ${APP_NAME} to start at login"
    else
      echo "note: login item registration failed — ${APP_NAME} still works, just without launch-at-login. Open it once to retry (it retries on every startup)."
    fi
    TRAY_EXE="${DEST}/Contents/Library/LoginItems/Tray.app/Contents/MacOS/${TRAY_BIN_NAME}"
    if [ -x "$TRAY_EXE" ]; then
      nohup "$TRAY_EXE" >/dev/null 2>&1 &
      disown 2>/dev/null || true
    fi
  else
    echo "Building the tray helper for ${OS}..."
    if ! make tray; then
      if [ "$OS" = "linux" ]; then
        echo "error: tray build failed. On Linux this is usually a missing appindicator dev package:" >&2
        echo "  sudo apt-get install -y libayatana-appindicator3-dev" >&2
        echo "(or libappindicator3-dev on older distros — see .github/workflows/app-build.yml for the Ubuntu package CI uses)." >&2
      fi
      exit 1
    fi

    TRAY_ARTIFACT="cmd/agent-winglet-tray/build/bin/$(tray_build_artifact "$OS")"
    if [ ! -e "$TRAY_ARTIFACT" ]; then
      echo "error: expected tray build output at ${TRAY_ARTIFACT} but it's not there." >&2
      exit 1
    fi

    TRAY_DEST="$(tray_install_path "$OS")"
    # Stop any already-running instance first — Windows won't let a running
    # .exe be overwritten, and this avoids two copies racing to register the
    # same autostart entry a moment from now.
    stop_tray
    mkdir -p "$(dirname "$TRAY_DEST")"
    cp "$TRAY_ARTIFACT" "$TRAY_DEST"
    chmod +x "$TRAY_DEST" 2>/dev/null || true
    TRAY_EXE="$(tray_executable_path "$OS")"

    if [ "$OS" = "linux" ]; then
      linux_register_tray_autostart "$TRAY_EXE"
      echo "Registered ${APP_NAME} to start at login (XDG autostart)"
    else
      windows_register_tray_autostart "$TRAY_EXE"
    fi
    echo "Installed/updated tray helper: ${TRAY_DEST}"

    # Launch it now, detached from this script, so the tray icon appears
    # right away instead of only at the next login.
    nohup "$TRAY_EXE" >/dev/null 2>&1 &
    disown 2>/dev/null || true
  fi
fi

echo ""
echo "Done. Re-run this script any time to update (git pull first for the latest app code)."
