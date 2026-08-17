//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// ensureLinuxTrayAutostart writes a per-user XDG autostart entry for the
// tray helper on first run under a packaged (.deb) install. install.sh
// already writes this exact file directly at install time (see
// scripts/lib.sh's linux_register_tray_autostart) — a system package can't
// do the same at install time since it has no single user's home to write
// into (see SPEC.md's Ubuntu Package section), so the app registers it
// itself the first time it actually runs as some specific user instead.
//
// Idempotent and silent: an existing entry — from either this or
// install.sh's own identical file — is left untouched, and a failure to
// write is logged, not surfaced. The dashboard and tray both still work
// without launch-at-login, same as any other autostart failure elsewhere in
// this codebase (e.g. RegisterLoginItem's callers).
func ensureLinuxTrayAutostart() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	desktopPath := filepath.Join(home, ".config", "autostart", "winglet-tray.desktop")
	if _, err := os.Stat(desktopPath); err == nil {
		return
	}

	trayPath, err := trayExecutablePath()
	if err != nil {
		return
	}

	if err := os.MkdirAll(filepath.Dir(desktopPath), 0o755); err != nil {
		fmt.Println("agent-winglet-app: failed to register tray autostart:", err)
		return
	}
	// Field-for-field identical to scripts/lib.sh's
	// linux_register_tray_autostart — the two must stay in sync so an
	// install.sh install and a packaged install produce the same autostart
	// entry.
	content := fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=%s Tray
Comment=Background menu-bar helper for %s
Exec=%s
X-GNOME-Autostart-enabled=true
NoDisplay=true
`, appName, appName, trayPath)
	if err := os.WriteFile(desktopPath, []byte(content), 0o644); err != nil {
		fmt.Println("agent-winglet-app: failed to register tray autostart:", err)
	}
}
