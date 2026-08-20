#!/bin/sh
# nfpm postinstall — deliberately does not touch any user's home directory
# (see nfpm.yaml.tmpl's doc comment): only refreshes system-wide desktop/
# icon caches, best-effort, so Winglet shows up in the launcher right away
# instead of only after the next cache refresh.
set -e

command -v update-desktop-database >/dev/null 2>&1 && update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
command -v gtk-update-icon-cache >/dev/null 2>&1 && gtk-update-icon-cache -f -t /usr/share/icons/hicolor >/dev/null 2>&1 || true

exit 0
