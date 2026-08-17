#!/bin/sh
# nfpm preremove — best-effort: stop any running tray helper so a removed
# package doesn't leave an orphaned process around. Not fatal if nothing is
# running (the common case) or if pkill isn't available.
set -e

command -v pkill >/dev/null 2>&1 && pkill -f /opt/winglet/agent-winglet-tray >/dev/null 2>&1 || true

exit 0
