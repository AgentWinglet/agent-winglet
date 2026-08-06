package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// toastWidth/toastHeight size the notification window: a small card
// centered on the primary screen — corner placement (bottom-right, the
// usual OS-notification spot) was tried first and dropped: Wails v2 has no
// work-area query to place above a Dock/taskbar precisely, only Screen.Size
// (the full screen), so a fixed margin either clipped under a larger Dock or
// left an odd gap under a smaller one. Centered needs no such guess.
// toastDuration is the fallback auto-dismiss — the frontend's own actions
// (Quit() on "Got it", Escape, or "Never show compact nudges") usually win;
// this just guarantees the window can't outlive its message if neither
// fires. Longer than a corner toast's would be, since this one asks the
// user to actually read and decide, not just glance and move on.
const (
	toastWidth    = 420
	toastHeight   = 156
	toastDuration = 20 * time.Second
)

// runToast is the entire lifecycle of a --toast invocation (see main.go):
// parse the payload the tray forwarded from internal/appipc's Notify
// command, show one small frameless always-on-top window with it, and exit
// once it's dismissed. A fresh process per notification — rather than a
// notifier kept running in the background — mirrors how ensureTrayRunning
// already launches this same binary on demand, and sidesteps keeping a
// second long-lived Wails runtime around just to wait for rare nudges.
func runToast(payload string) {
	var t Toast
	if err := json.Unmarshal([]byte(payload), &t); err != nil {
		fmt.Fprintln(os.Stderr, "--toast: invalid payload:", err)
		os.Exit(1)
	}
	t.Active = true
	app := NewApp(&t)

	err := wails.Run(&options.App{
		Title:            "Winglet",
		Width:            toastWidth,
		Height:           toastHeight,
		DisableResize:    true,
		Frameless:        true,
		AlwaysOnTop:      true,
		StartHidden:      true, // toastStartup centers it before revealing, so there's no jump from wherever it would otherwise first paint
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		OnStartup: app.toastStartup,
		Bind: []interface{}{
			app,
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err.Error())
	}
}

// toastStartup centers the window on screen, reveals it (see StartHidden
// above), and arms the fallback auto-dismiss timer. Deliberately does none
// of what App.startup does for the real dashboard (IPC listener, tray-launch
// check, login-item registration) — this process exists only to show one
// message.
func (a *App) toastStartup(ctx context.Context) {
	a.ctx = ctx

	wailsruntime.WindowCenter(ctx)
	wailsruntime.WindowShow(ctx)

	time.AfterFunc(toastDuration, func() {
		wailsruntime.Quit(ctx)
	})
}
