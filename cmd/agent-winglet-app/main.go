// Command agent-winglet-app is a small cross-platform desktop dashboard
// (Wails — Go backend, OS-native webview frontend) that shows the savings
// receipt already computed by cmd/ledger-hook, without the user reading
// JSON files or hook stdout by hand.
//
// The menu-bar/tray glance icon lives in a separate binary,
// cmd/agent-winglet-tray, not in this one: spiking getlantern/systray
// alongside Wails in the same binary produces a link-time failure on macOS —
// both packages independently declare an Objective-C class named AppDelegate
// (systray_darwin.m vs. Wails' internal/frontend/desktop/darwin/
// AppDelegate.h), which collides as a duplicate symbol. Rather than fork and
// patch one of the two libraries just to rename a class, the tray runs as
// its own process and talks to this one over internal/appipc — see that
// package's doc comment and cmd/agent-winglet-tray's for the other half of
// this.
package main

import (
	"embed"
	"fmt"
	"os"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	// Headless CLI modes for install.sh/uninstall.sh — SMAppService's login
	// item registration can only be called by the app whose bundle owns it
	// (see loginitem_darwin.go), so these give the installer a way to
	// register/unregister it without ever opening a window. No-ops on
	// non-darwin (see loginitem_other.go); the flags are accepted there too
	// so install.sh doesn't need per-OS branching to know whether to pass
	// them.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--register-login-item":
			if err := RegisterLoginItem(); err != nil {
				fmt.Fprintln(os.Stderr, "register-login-item:", err)
				os.Exit(1)
			}
			return
		case "--unregister-login-item":
			if err := UnregisterLoginItem(); err != nil {
				fmt.Fprintln(os.Stderr, "unregister-login-item:", err)
				os.Exit(1)
			}
			return
		case "--login-item-status":
			fmt.Println(LoginItemStatus())
			return
		}
	}

	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Winglet",
		Width:     920,
		Height:    640,
		MinWidth:  680,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			// TitleBar left at Wails' default (native chrome) rather than a
			// frameless custom title bar: a hand-drawn traffic-light window
			// control reads as fake the moment its inset/spacing doesn't
			// exactly match the real thing, which isn't worth risking here.
			About: &mac.AboutInfo{
				Title:   "Winglet",
				Message: "Local-only dashboard for agent-winglet's Claude Code hooks.",
			},
			// Lets the sidebar's backdrop-filter blur (style.css, gated on
			// data-os="darwin") show real desktop vibrancy behind it instead
			// of just a translucent solid color. Windows/Linux are
			// unaffected: this is a Mac-only option, and the CSS blur it
			// enables is itself gated to data-os=darwin.
			WindowIsTranslucent: true,
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
