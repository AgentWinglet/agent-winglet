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

# Register this project in the global registry so the desktop app (agent-winglet-app)
# can find every project's lifetime.stats.json without a daemon or IPC — it just
# reads the same files install.sh and the hooks already write. Flat JSON array of
# absolute project paths, deduped on install; a project directory that's later
# moved/deleted is skipped on read and pruned lazily the next time install.sh runs.
REGISTRY_DIR="${HOME}/.agent-winglet"
REGISTRY_FILE="${REGISTRY_DIR}/projects.json"
mkdir -p "$REGISTRY_DIR"
if [ ! -f "$REGISTRY_FILE" ]; then
  echo '[]' > "$REGISTRY_FILE"
fi
PROJECT_DIR="$(pwd -P)"

# Prune entries whose directory no longer exists (moved/deleted since a
# previous install.sh run) before adding this one — the lazy-prune point
# named above. A shell loop, not jq alone, since jq has no filesystem access.
PRUNED_TMP="$(mktemp)"
echo '[]' > "$PRUNED_TMP"
while IFS= read -r existing; do
  [ -d "$existing" ] || continue
  jq --arg dir "$existing" '. + [$dir]' "$PRUNED_TMP" > "${PRUNED_TMP}.next"
  mv "${PRUNED_TMP}.next" "$PRUNED_TMP"
done < <(jq -r '.[]' "$REGISTRY_FILE")

REGISTRY_TMP="$(mktemp)"
jq --arg dir "$PROJECT_DIR" 'if index($dir) then . else . + [$dir] end' \
  "$PRUNED_TMP" > "$REGISTRY_TMP"
mv "$REGISTRY_TMP" "$REGISTRY_FILE"
rm -f "$PRUNED_TMP"

echo "Installed. Hook binary: ${HOOK_PATH}"
echo "Updated: ${SETTINGS_FILE}"
echo "Registered project in: ${REGISTRY_FILE}"
