package main

import (
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// OpenDataFolder reveals ~/.agent-winglet — the saved usage stats, project
// registry, and preferences UninstallWinglet's doc comment describes as the
// one thing uninstalling deliberately leaves behind — in the OS's file
// manager, for support requests that need a look at what's on disk.
// MkdirAll first since a signed-out or freshly-installed app may not have
// written anything there yet, and revealing a folder that doesn't exist just
// errors on every platform.
func (a *App) OpenDataFolder() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(home, ".agent-winglet")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	switch goruntime.GOOS {
	case "darwin":
		return exec.Command("open", dir).Start()
	case "windows":
		return exec.Command("explorer", dir).Start()
	default:
		return exec.Command("xdg-open", dir).Start()
	}
}

// OpenReleaseNotes opens the repository's GitHub Releases index — every past
// release, not just the single newer one CheckForUpdate/OpenUpdateRelease
// deal in — so About can link somewhere useful even when the app is already
// current.
func (a *App) OpenReleaseNotes() {
	if a.ctx != nil {
		wailsruntime.BrowserOpenURL(a.ctx, updateReleaseURL)
	}
}
