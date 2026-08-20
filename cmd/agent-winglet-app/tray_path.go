package main

import (
	"errors"
	"path/filepath"
	"runtime"
)

var errUnsupportedOS = errors.New("agent-winglet-app: unsupported OS")

// appName and trayBinName must match scripts/lib.sh's APP_NAME/TRAY_BIN_NAME,
// and trayExecutablePath must match the Makefile's nest-tray-darwin target.
// The dashboard can't source that shell helper, so this is kept intentionally
// tiny and cross-referenced here so the two don't silently drift — mirror
// image of cmd/agent-winglet-tray/app_path.go's appExecutablePath, which
// locates this binary from the tray's side.
const (
	appName     = "Winglet"
	trayBinName = "agent-winglet-tray"
)

// trayExecutablePath returns the installed tray helper's path, or an error
// outside macOS, where Winglet no longer installs a tray helper.
func trayExecutablePath() (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errUnsupportedOS
	}
	// Nested inside this app's own bundle, not standalone — see the Makefile's
	// nest-tray-darwin target for why (SMAppService login items must live inside
	// the app that registers them).
	return filepath.Join("/Applications", appName+".app", "Contents", "Library", "LoginItems", "Tray.app", "Contents", "MacOS", trayBinName), nil
}
