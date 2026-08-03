#!/usr/bin/env bash
# Reverses install.sh: strips every ledger-hook entry out of Claude Code's
# hook config and removes the installed app (both, by default), then
# optionally removes the hook binary and/or agent-winglet's data files too.
#
# Usage:
#   ./uninstall.sh                 # remove hook wiring + the installed app
#   ./uninstall.sh --hook-only     # just the hook wiring
#   ./uninstall.sh --app-only      # just the app
#   ./uninstall.sh --local         # remove hook wiring from ./.claude/settings.json
#                                   # instead of ~/.claude/settings.json
#                                   # (--hook-only scope only; the app has no
#                                   # such concept)
#   ./uninstall.sh --purge-binary  # also delete the installed ledger-hook binary
#   ./uninstall.sh --purge-data    # also delete ~/.agent-winglet (global registry/config)
#                                   # and every registered project's .claude/agent-winglet/
#                                   # (lists what it's about to delete and asks for
#                                   # confirmation unless -y/--yes is also passed)
#   ./uninstall.sh -y|--yes        # skip the --purge-data confirmation prompt
#
# Flags can be combined, e.g. `./uninstall.sh --purge-binary --purge-data -y`
# for a full teardown of everything agent-winglet ever touched.
#
# Unlike install.sh's app step, this doesn't need to be run from inside a
# repo checkout — it only removes files from known OS-specific install
# locations, it doesn't build anything.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=scripts/lib.sh
source "${SCRIPT_DIR}/scripts/lib.sh"

BINARY_NAME="ledger-hook"

WANT_HOOK=1
WANT_APP=1
SETTINGS_FILE="${HOME}/.claude/settings.json"
SCOPE_DESC="from ~/.claude/settings.json (global)"
PURGE_BINARY=0
PURGE_DATA=0
ASSUME_YES=0

for arg in "$@"; do
  case "$arg" in
    --local)
      SETTINGS_FILE=".claude/settings.json"
      SCOPE_DESC="from ./.claude/settings.json (this project only)"
      ;;
    --hook-only) WANT_APP=0 ;;
    --app-only) WANT_HOOK=0 ;;
    --purge-binary) PURGE_BINARY=1 ;;
    --purge-data) PURGE_DATA=1 ;;
    -y|--yes) ASSUME_YES=1 ;;
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

##############################################################################
# Hook
##############################################################################
if [ "$WANT_HOOK" = "1" ]; then
  if ! command -v jq >/dev/null 2>&1; then
    echo "error: jq is required to edit hook config. Install it first (e.g. brew install jq)." >&2
    exit 1
  fi

  if [ -f "$SETTINGS_FILE" ]; then
    BEFORE="$(jq '[.hooks // {} | .. | objects | select(has("command")) | .command | select((split("/") | last) == "'"${BINARY_NAME}"'")] | length' "$SETTINGS_FILE")"
    if [ "$BEFORE" = "0" ]; then
      echo "No ${BINARY_NAME} hook entries found in ${SETTINGS_FILE} — nothing to remove there."
    else
      TMP_FILE="$(mktemp)"
      # Drop any hook command entry whose basename is ledger-hook (matching by
      # basename rather than an exact path, same as internal/registry's
      # containsLedgerHookCommand, since the absolute GOBIN path can vary
      # machine-to-machine and even installer-run to installer-run). Matcher
      # entries left with an empty hooks array are dropped entirely, and event
      # keys left with an empty array are dropped too, so the file doesn't
      # accumulate empty scaffolding across repeated installs/uninstalls.
      jq '
        .hooks |= with_entries(
          .value |= [
            .[] | (. + {hooks: [.hooks[]? | select((.command // "" | (split("/") | last)) != "'"${BINARY_NAME}"'")]}) | select((.hooks | length) > 0)
          ]
        )
        | .hooks |= with_entries(select((.value | length) > 0))
      ' "$SETTINGS_FILE" > "$TMP_FILE"
      mv "$TMP_FILE" "$SETTINGS_FILE"
      echo "Removed ${BEFORE} ${BINARY_NAME} hook entr$([ "$BEFORE" = "1" ] && echo y || echo ies) ${SCOPE_DESC}."
    fi
  else
    echo "${SETTINGS_FILE} doesn't exist — nothing to remove there."
  fi

  if [ "$PURGE_BINARY" = "1" ]; then
    GOBIN="$(go env GOBIN 2>/dev/null || true)"
    if [ -z "$GOBIN" ]; then
      GOBIN="$(go env GOPATH 2>/dev/null || echo "${HOME}/go")/bin"
    fi
    HOOK_PATH="${GOBIN}/${BINARY_NAME}"
    if [ -f "$HOOK_PATH" ]; then
      rm -f "$HOOK_PATH"
      echo "Removed binary: ${HOOK_PATH}"
    else
      echo "No binary found at ${HOOK_PATH} — nothing to remove."
    fi
  fi
fi

##############################################################################
# App
##############################################################################
if [ "$WANT_APP" = "1" ]; then
  OS="$(detect_os)"
  if [ "$OS" = "unknown" ]; then
    if [ "$WANT_HOOK" = "0" ]; then
      echo "error: don't know how to uninstall the app on '$(uname -s)'." >&2
      exit 1
    fi
    echo "note: don't know how to uninstall the app on '$(uname -s)' — skipping app removal."
  else
    FOUND_ANY=0
    case "$OS" in
      darwin)
        for DEST in "$(app_install_path darwin)" "$(app_install_path_alt darwin)"; do
          if [ -e "$DEST" ]; then
            FOUND_ANY=1
            if pgrep -f "/Contents/MacOS/${APP_NAME}" >/dev/null 2>&1; then
              echo "note: ${APP_NAME} is currently running — quit it if you don't want a" \
                   "still-running copy left over after this removes it from disk."
            fi
            rm -rf "$DEST"
            echo "Removed app: ${DEST}"
          fi
        done
        ;;
      linux)
        BIN="$(app_install_path linux)"
        DESKTOP="${HOME}/.local/share/applications/${APP_NAME}.desktop"
        ICON_DIR="${HOME}/.local/share/${APP_NAME}"
        for DEST in "$BIN" "$DESKTOP" "$ICON_DIR"; do
          if [ -e "$DEST" ]; then
            FOUND_ANY=1
            rm -rf "$DEST"
            echo "Removed: ${DEST}"
          fi
        done
        command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database "${HOME}/.local/share/applications" || true
        ;;
      windows)
        EXE="$(app_install_path windows)"
        INSTALL_DIR="$(dirname "$EXE")"
        if [ -e "$EXE" ]; then
          FOUND_ANY=1
          rm -rf "$INSTALL_DIR"
          echo "Removed app: ${INSTALL_DIR}"
        fi
        windows_remove_shortcut
        ;;
    esac
    if [ "$FOUND_ANY" = "0" ]; then
      echo "No installed app found — nothing to remove."
    fi
  fi
fi

##############################################################################
# Data
##############################################################################
if [ "$PURGE_DATA" = "1" ]; then
  if ! command -v jq >/dev/null 2>&1; then
    echo "error: jq is required to read the project registry for --purge-data. Install it first (e.g. brew install jq)." >&2
    exit 1
  fi

  GLOBAL_DIR="${HOME}/.agent-winglet"
  REGISTRY_FILE="${GLOBAL_DIR}/projects.json"

  PROJECT_DIRS=()
  if [ -f "$REGISTRY_FILE" ]; then
    while IFS= read -r dir; do
      [ -n "$dir" ] && [ -d "${dir}/.claude/agent-winglet" ] && PROJECT_DIRS+=("${dir}/.claude/agent-winglet")
    done < <(jq -r '.[]?' "$REGISTRY_FILE" 2>/dev/null || true)
  fi

  echo ""
  echo "--purge-data will permanently delete:"
  [ -d "$GLOBAL_DIR" ] && echo "  ${GLOBAL_DIR}  (global registry + quiet-mode config)"
  for d in "${PROJECT_DIRS[@]:-}"; do
    [ -n "$d" ] && echo "  ${d}  (that project's savings ledger/stats history)"
  done
  if [ ! -d "$GLOBAL_DIR" ] && [ "${#PROJECT_DIRS[@]}" -eq 0 ]; then
    echo "  (nothing found — no global or per-project data on this machine)"
  fi
  echo ""

  if [ "$ASSUME_YES" != "1" ]; then
    read -r -p "Proceed with deleting the above? [y/N] " REPLY
    case "$REPLY" in
      [yY]|[yY][eE][sS]) ;;
      *) echo "Skipped data purge."; PROJECT_DIRS=(); GLOBAL_DIR="" ;;
    esac
  fi

  [ -n "$GLOBAL_DIR" ] && [ -d "$GLOBAL_DIR" ] && rm -rf "$GLOBAL_DIR" && echo "Deleted ${GLOBAL_DIR}"
  for d in "${PROJECT_DIRS[@]:-}"; do
    [ -n "$d" ] && [ -d "$d" ] && rm -rf "$d" && echo "Deleted ${d}"
  done
fi

echo ""
echo "Uninstall complete."
