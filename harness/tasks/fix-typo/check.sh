#!/usr/bin/env bash
# Success = the typo is fixed and the fixture still builds.
set -euo pipefail

if grep -q '"helo,' greeting.go; then
  echo "check: greeting.go still contains the typo" >&2
  exit 1
fi

if ! grep -q '"hello, %s"' greeting.go; then
  echo "check: greeting.go does not contain the expected fixed string" >&2
  exit 1
fi

go build ./...
