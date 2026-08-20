//go:build darwin

package main

import "path/filepath"

const appName = "Winglet"

// appExecutablePath returns the installed dashboard executable inside the
// standard macOS app bundle. It mirrors cmd/agent-winglet-app/tray_path.go's
// nested-helper path from the tray side.
func appExecutablePath() (string, error) {
	return filepath.Join("/Applications", appName+".app", "Contents", "MacOS", appName), nil
}
