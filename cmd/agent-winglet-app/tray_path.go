package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
)

var errUnsupportedOS = errors.New("agent-winglet-app: unsupported OS")

// appName and trayBinName must match scripts/lib.sh's APP_NAME/TRAY_BIN_NAME,
// and trayExecutablePath must match the per-OS layout scripts/lib.sh's
// tray_install_path (linux/windows) and the Makefile's nest-tray-darwin
// target (darwin) install it to. The dashboard can't source that shell
// helper, so this is a from-scratch reimplementation kept intentionally
// tiny and cross-referenced here so the two don't silently drift — mirror
// image of cmd/agent-winglet-tray/app_path.go's appExecutablePath, which
// locates this binary from the tray's side.
const (
	appName     = "Winglet"
	trayBinName = "agent-winglet-tray"
)

// trayExecutablePath returns the installed tray helper's path, or an error
// if the current OS isn't one install.sh knows how to install it to.
func trayExecutablePath() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		// Nested inside this app's own bundle, not standalone — see the
		// Makefile's nest-tray-darwin target for why (SMAppService login
		// items must live inside the app that registers them).
		return filepath.Join("/Applications", appName+".app", "Contents", "Library", "LoginItems", "Tray.app", "Contents", "MacOS", trayBinName), nil
	case "linux", "windows":
		// install.sh puts the tray helper right next to this binary
		// (~/.local/bin/{Winglet,agent-winglet-tray} on linux,
		// %LOCALAPPDATA%\Winglet\{Winglet,agent-winglet-tray}.exe on
		// windows), and so does the packaged install (a Debian package's
		// /opt/winglet/{Winglet,agent-winglet-tray}, or the Windows NSIS
		// installer's $INSTDIR) — so resolving relative to this process'
		// own (symlink-resolved, for /usr/bin/winglet -> /opt/winglet/
		// Winglet) executable path covers every install method with one
		// lookup, instead of hardcoding install.sh's own layout and
		// silently missing a packaged install's different one.
		binName := trayBinName
		if runtime.GOOS == "windows" {
			binName += ".exe"
		}
		if self, err := os.Executable(); err == nil {
			if resolved, err := filepath.EvalSymlinks(self); err == nil {
				return filepath.Join(filepath.Dir(resolved), binName), nil
			}
			return filepath.Join(filepath.Dir(self), binName), nil
		}
		return legacyTrayExecutablePath()
	default:
		return "", errUnsupportedOS
	}
}

// legacyTrayExecutablePath is trayExecutablePath's fallback for the rare
// case os.Executable() itself fails — install.sh's own fixed layout, same
// as before the sibling-of-executable lookup above was added.
func legacyTrayExecutablePath() (string, error) {
	switch runtime.GOOS {
	case "linux":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, ".local", "bin", trayBinName), nil
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", err
			}
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(localAppData, appName, trayBinName+".exe"), nil
	default:
		return "", errUnsupportedOS
	}
}
