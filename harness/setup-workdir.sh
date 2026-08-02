#!/usr/bin/env bash
# Prepares a working directory for one paired-run trial: wipes it, copies
# the task's fixture/ into it, and configures the ledger hook according to
# the requested variant. Run this before each `measure` invocation — every
# trial should start from identical fixture state, regardless of variant.
#
# Usage: setup-workdir.sh <task-dir> <workdir> <hook|control>
set -euo pipefail

TASK_DIR="$1"
WORK_DIR="$2"
VARIANT="$3"

if [ "$VARIANT" != "hook" ] && [ "$VARIANT" != "control" ]; then
  echo "error: variant must be 'hook' or 'control', got '$VARIANT'" >&2
  exit 1
fi

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LEDGER_HOOK="${REPO_ROOT}/bin/ledger-hook"

if [ "$VARIANT" = "hook" ] && [ ! -x "$LEDGER_HOOK" ]; then
  echo "error: $LEDGER_HOOK not found. Run 'make build' first." >&2
  exit 1
fi

rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR"

if [ -d "${TASK_DIR}/fixture" ]; then
  cp -R "${TASK_DIR}/fixture/." "$WORK_DIR/"
fi

mkdir -p "${WORK_DIR}/.claude"
if [ "$VARIANT" = "hook" ]; then
  cat > "${WORK_DIR}/.claude/settings.json" <<EOF
{
  "hooks": {
    "PostToolUse": [
      {"matcher": "Bash", "hooks": [{"type": "command", "command": "${LEDGER_HOOK}"}]}
    ],
    "SessionStart": [
      {"hooks": [{"type": "command", "command": "${LEDGER_HOOK}"}]}
    ],
    "PostCompact": [
      {"hooks": [{"type": "command", "command": "${LEDGER_HOOK}"}]}
    ]
  }
}
EOF
else
  echo '{}' > "${WORK_DIR}/.claude/settings.json"
fi
