//go:build !darwin

package main

// RegisterLoginItem/UnregisterLoginItem are no-ops outside macOS because
// Winglet only ships a tray helper there. See loginitem_darwin.go for why
// macOS is different (SMAppService can only be called by the app whose bundle
// owns the nested login item).

func RegisterLoginItem() error {
	return nil
}

func UnregisterLoginItem() error {
	return nil
}

func LoginItemStatus() string {
	return "unsupported"
}
