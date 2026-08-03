// Command agent-winglet-app is the v1 step 3 desktop dashboard: a small
// cross-platform window (Wails — Go backend, OS-native webview frontend)
// that shows the savings receipt already computed by cmd/ledger-hook,
// without the user reading JSON files or hook stdout by hand. See
// agent-winglet-v1-step3-spec.md for the full design rationale.
//
// No system-tray/menu-bar glance icon in v1: spiking getlantern/systray
// alongside Wails in the same binary produces a link-time failure on
// macOS — both packages independently declare an Objective-C class named
// AppDelegate (systray_darwin.m vs. Wails' internal/frontend/desktop/
// darwin/AppDelegate.h), which collides as a duplicate symbol. That's the
// spec's §6 open risk resolving toward its own documented fallback: Dock/
// taskbar icon only.
package main

import (
	"embed"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	app := NewApp()

	err := wails.Run(&options.App{
		Title:     "Agent Winglet",
		Width:     920,
		Height:    640,
		MinWidth:  680,
		MinHeight: 480,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		OnStartup:        app.startup,
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			// TitleBar left at Wails' default (native chrome) rather than a
			// frameless custom title bar — see this app's design spec §5's
			// anti-pattern note against hand-drawn traffic-light controls.
			About: &mac.AboutInfo{
				Title:   "Agent Winglet",
				Message: "Local-only dashboard for agent-winglet's Claude Code hooks.",
			},
		},
	})
	if err != nil {
		println("Error:", err.Error())
	}
}
