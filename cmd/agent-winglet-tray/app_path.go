package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// appName must match scripts/lib.sh's APP_NAME, and appExecutablePath must
// match the per-OS layout scripts/lib.sh's app_install_path installs to (the
// tray can't source that shell helper, so this is a from-scratch
// reimplementation kept intentionally tiny and cross-referenced here so the
// two don't silently drift).
const appName = "Winglet"

// appExecutablePath returns the installed dashboard binary's path, or an
// error if the current OS isn't one install.sh knows how to install to.
// Unlike scripts/lib.sh's app_install_path (which returns the darwin .app
// bundle itself), this returns the actual executable inside it — exec.Command
// needs a runnable file, not a directory.
func appExecutablePath() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join("/Applications", appName+".app", "Contents", "MacOS", appName), nil
	case "linux":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "bin", appName), nil
	case "windows":
		// Mirrors scripts/lib.sh's windows_local_app_dir: %LOCALAPPDATA%\<app>\<app>.exe.
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(localAppData, appName, appName+".exe"), nil
	default:
		return "", errUnsupportedOS
	}
}
