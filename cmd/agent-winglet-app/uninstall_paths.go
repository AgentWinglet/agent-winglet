package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// This file is UninstallWinglet's (app.go) path/process counterpart to
// scripts/lib.sh's app_install_path/app_install_path_alt helpers, kept in sync
// with that shell script by hand since uninstall.sh can't be sourced from here
// and, unlike the app bundle it targets, isn't guaranteed to still be on disk
// by the time someone clicks Uninstall — see UninstallWinglet's own doc
// comment for why this doesn't just shell out to it instead.

// appInstallPaths returns every location install.sh is known to have put the
// app's own installed artifact, mirroring scripts/lib.sh's
// app_install_path/app_install_path_alt. darwin has two (the legacy
// ~/Applications fallback install.sh and uninstall.sh both still check);
// linux/windows have exactly one.
func appInstallPaths() ([]string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	switch runtime.GOOS {
	case "darwin":
		return []string{
			filepath.Join("/Applications", appName+".app"),
			filepath.Join(home, "Applications", appName+".app"),
		}, nil
	case "linux":
		return []string{filepath.Join(home, ".local", "bin", appName)}, nil
	case "windows":
		return []string{filepath.Join(windowsLocalAppDir(home), appName, appName+".exe")}, nil
	default:
		return nil, errUnsupportedOS
	}
}

func windowsLocalAppDir(home string) string {
	if v := os.Getenv("LOCALAPPDATA"); v != "" {
		return v
	}
	return filepath.Join(home, "AppData", "Local")
}

// windowsStartMenuProgramsDir mirrors scripts/lib.sh's
// windows_local_app_dir()/../Roaming/Microsoft/Windows/Start Menu/Programs —
// where install.sh's windows_create_shortcut puts the app's own shortcut.
func windowsStartMenuProgramsDir(home string) string {
	return filepath.Join(windowsLocalAppDir(home), "..", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs")
}

func linuxDesktopEntryPath(home string) string {
	return filepath.Join(home, ".local", "share", "applications", appName+".desktop")
}

func linuxIconDir(home string) string {
	return filepath.Join(home, ".local", "share", appName)
}

func windowsAppShortcutPath(home string) string {
	return filepath.Join(windowsStartMenuProgramsDir(home), appName+".lnk")
}

// stopTrayHelper best-effort kills any currently-running tray helper
// process — mirrors scripts/lib.sh's stop_tray. Never fatal: nothing running
// is the common case, not an error, same as the shell version.
func stopTrayHelper() {
	if runtime.GOOS != "darwin" {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill.exe", "/IM", trayBinName+".exe", "/F").Run()
		return
	}
	_ = exec.Command("pkill", "-f", trayBinName).Run()
}

// removeAppShortcut removes the Start Menu shortcut
// scripts/lib.sh's windows_create_shortcut wrote at install time. No darwin
// or linux equivalent: darwin launches straight from /Applications, and
// linux's .desktop entry (removed alongside the binary in UninstallWinglet)
// already covers app-launcher visibility there.
func removeAppShortcut(home string) {
	if runtime.GOOS != "windows" {
		return
	}
	_ = os.Remove(windowsAppShortcutPath(home))
}
