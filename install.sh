#!/usr/bin/env bash
# Installs the agent-winglet Session Ledger hook globally, into
# ~/.claude/settings.json — Claude Code merges user-level hooks with any
# project-level ones, so this makes the hook active for every project's
# Claude Code sessions, not just the one you happen to run this from.
#
# Pass --local to install into the current directory's project instead
# (./.claude/settings.json), scoped to just that project, as this script did
# before the hook became global by default.
#
# Migration note: if you previously ran this script's old per-project flow in
# a project, that project's .claude/settings.json still has its own
# ledger-hook entry. Once the global hook is installed too, that project's
# hook fires twice per event (once via the global entry, once via the local
# one) — remove the old entry from that project's .claude/settings.json to
# avoid double-counting stats and corrupting the ledger's turn tracking.
set -euo pipefail

REPO_URL="github.com/umitkaanusta/agent-winglet"
BINARY_NAME="ledger-hook"

SETTINGS_FILE="${HOME}/.claude/settings.json"
SCOPE_DESC="globally (~/.claude/settings.json)"
if [ "${1:-}" = "--local" ]; then
  SETTINGS_FILE=".claude/settings.json"
  SCOPE_DESC="for this project only (./.claude/settings.json)"
fi

if ! command -v go >/dev/null 2>&1; then
  echo "error: go is not installed. Install Go first: https://go.dev/dl/" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq is required to merge hook config. Install it first (e.g. brew install jq)." >&2
  exit 1
fi

echo "Installing ${BINARY_NAME} ${SCOPE_DESC}..."
go install "${REPO_URL}/cmd/${BINARY_NAME}@latest"

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

echo "Installed. Hook binary: ${HOOK_PATH}"
echo "Updated: ${SETTINGS_FILE}"
