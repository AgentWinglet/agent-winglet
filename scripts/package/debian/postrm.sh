#!/bin/sh
# nfpm postremove — refreshes the desktop database, best-effort, after
# dpkg has already removed the package-owned files (nfpm.yaml.tmpl's
# contents). Deliberately does not touch any user's home directory: a
# per-user tray autostart entry written by the app on first launch (see
# cmd/agent-winglet-app/autostart_linux.go) is left in place, same as
# install.sh/uninstall.sh's own --purge-data being opt-in rather than
# automatic for user data.
set -e

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database /usr/share/applications >/dev/null 2>&1 || true

exit 0
