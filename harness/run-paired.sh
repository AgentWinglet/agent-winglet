#!/usr/bin/env bash
# Runs one full paired-run trial for a task: a control run (no hook) and a
# hook run, back to back, both scored and appended to the same results log.
# This is the harness/README.md-documented entry point for §5's measurement
# gate — repeat it across tasks and multiple trials per task before drawing
# any usage_per_solve conclusion from a single pair.
#
# Usage: run-paired.sh <task-name> [results-path]
set -euo pipefail

TASK_NAME="$1"
RESULTS="${2:-harness/results.jsonl}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TASK_DIR="${REPO_ROOT}/harness/tasks/${TASK_NAME}"

if [ ! -d "$TASK_DIR" ]; then
  echo "error: no task at $TASK_DIR" >&2
  exit 1
fi

MEASURE="${REPO_ROOT}/bin/measure"
if [ ! -x "$MEASURE" ]; then
  echo "error: $MEASURE not found. Run 'make build' first." >&2
  exit 1
fi

WORK_BASE="$(mktemp -d)"
trap 'rm -rf "$WORK_BASE"' EXIT

for VARIANT in control hook; do
  WORK_DIR="${WORK_BASE}/${VARIANT}"
  echo "--- ${TASK_NAME} / ${VARIANT} ---"
  "${REPO_ROOT}/harness/setup-workdir.sh" "$TASK_DIR" "$WORK_DIR" "$VARIANT"
  "$MEASURE" -task "$TASK_DIR" -workdir "$WORK_DIR" -variant "$VARIANT" -results "$RESULTS"
done
