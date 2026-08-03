#!/usr/bin/env bash
# Installs the agent-winglet Session Ledger hook into the current directory's
# Claude Code project. Run this from the root of the project you want the
# hook active in — not from the agent-winglet repo itself.
set -euo pipefail

REPO_URL="github.com/umitkaanusta/agent-winglet"
BINARY_NAME="ledger-hook"

if ! command -v go >/dev/null 2>&1; then
  echo "error: go is not installed. Install Go first: https://go.dev/dl/" >&2
  exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
  echo "error: jq is required to merge hook config. Install it first (e.g. brew install jq)." >&2
  exit 1
fi

echo "Installing ${BINARY_NAME}..."
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

mkdir -p .claude
SETTINGS_FILE=".claude/settings.json"
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

echo "Installed. Hook binary: ${HOOK_PATH}"
echo "Updated: ${SETTINGS_FILE}"
