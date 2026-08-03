// Command agent-winglet-app is a small cross-platform desktop dashboard
// (Wails — Go backend, OS-native webview frontend) that shows the savings
// receipt already computed by cmd/ledger-hook, without the user reading
// JSON files or hook stdout by hand.
//
// No system-tray/menu-bar glance icon: spiking getlantern/systray alongside
// Wails in the same binary produces a link-time failure on macOS — both
// packages independently declare an Objective-C class named AppDelegate
// (systray_darwin.m vs. Wails' internal/frontend/desktop/darwin/
// AppDelegate.h), which collides as a duplicate symbol. Rather than fork
// and patch one of the two libraries just to rename a class, this ships
// with a Dock/taskbar icon only.
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
			// frameless custom title bar: a hand-drawn traffic-light window
			// control reads as fake the moment its inset/spacing doesn't
			// exactly match the real thing, which isn't worth risking here.
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
