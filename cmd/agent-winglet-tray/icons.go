package main

import (
	_ "embed"
	"runtime"
)

// Local copies rather than reaching into ../../branding across the module
// boundary — same convention cmd/agent-winglet-app/build/appicon.png already
// follows for the Wails app's own icon.
//
// Three files, not one, because getlantern/systray's cross-platform
// SetTemplateIcon(template, regular) call only gets macOS's "template" arg
// right automatically — the "regular" arg is passed through as-is to
// whichever OS-specific SetIcon happens to be compiled in, and Windows and
// Linux disagree on what format that needs: Windows loads it as a native
// icon resource and requires real .ico bytes, while Linux decodes it via
// gdk-pixbuf format-sniffing, which reliably supports .png but not .ico.
// Picking the right "regular" bytes per OS is therefore this file's job, not
// the library's.
var (
	//go:embed icons/template.png
	templateIconPNG []byte
	//go:embed icons/regular.ico
	regularIconICO []byte
	//go:embed icons/regular.png
	regularIconPNG []byte
)

// trayIcons returns the (template, regular) byte pair to hand to
// systray.SetTemplateIcon for the current OS.
func trayIcons() (template, regular []byte) {
	if runtime.GOOS == "windows" {
		return templateIconPNG, regularIconICO
	}
	return templateIconPNG, regularIconPNG
}
