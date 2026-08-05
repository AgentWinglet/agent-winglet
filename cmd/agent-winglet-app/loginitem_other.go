//go:build !darwin

package main

// RegisterLoginItem/UnregisterLoginItem are no-ops outside macOS —
// Windows/Linux register their tray autostart entry directly from
// install.sh/uninstall.sh (Startup-folder shortcut / XDG autostart), no
// in-app API call needed. See loginitem_darwin.go for why macOS is
// different (SMAppService can only be called by the app whose bundle owns
// the nested login item).

func RegisterLoginItem() error {
	return nil
}

func UnregisterLoginItem() error {
	return nil
}

func LoginItemStatus() string {
	return "unsupported"
}
