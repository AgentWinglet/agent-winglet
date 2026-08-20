#!/bin/sh
# nfpm postremove — refreshes the desktop database, best-effort, after
# dpkg has already removed the package-owned files (nfpm.yaml.tmpl's
# contents). Deliberately does not touch any user's home directory.
set -e

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database /usr/share/applications >/dev/null 2>&1 || true

exit 0
