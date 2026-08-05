// Command agent-winglet-tray is the menu-bar/tray helper for the Winglet
// dashboard app (cmd/agent-winglet-app). It's a separate binary rather than a
// second goroutine in the dashboard itself because getlantern/systray and
// Wails fail to link into the same binary on macOS — both declare an
// Objective-C class named AppDelegate, which collides as a duplicate symbol
// at link time (see cmd/agent-winglet-app/main.go's doc comment for the
// history). Running the tray as its own process sidesteps that entirely.
//
// install.sh registers this to launch at login (LaunchAgent on macOS, a
// Startup-folder shortcut on Windows, an XDG autostart entry on Linux), so
// the icon is meant to be there from login onward, independent of whether
// the dashboard window has been opened yet. It talks to the dashboard over
// internal/appipc: "Open Winglet" asks a running dashboard to show its
// window, or launches one if none is running; "Quit" tells a running
// dashboard to actually exit (bypassing its hide-on-close behavior) and then
// exits itself.
package main

import (
	"errors"
	"fmt"
	"net"
	"os/exec"

	"github.com/getlantern/systray"

	"github.com/umitkaanusta/agent-winglet/internal/appipc"
)

var errUnsupportedOS = errors.New("agent-winglet-tray: unsupported OS")

func main() {
	systray.Run(onReady, onExit)
}

func onReady() {
	template, regular := trayIcons()
	systray.SetTemplateIcon(template, regular)
	systray.SetTooltip("Winglet")

	mOpen := systray.AddMenuItem("Open Winglet", "Show the Winglet dashboard")
	systray.AddSeparator()
	mQuit := systray.AddMenuItem("Quit", "Quit Winglet")

	// serveLiveness lets the dashboard tell whether a tray is actually
	// around before it decides to hide on close instead of quitting — see
	// appipc.TrayRunning. Nothing is ever read off accepted connections, so
	// a failure to bind here (e.g. two tray instances racing) just means the
	// dashboard will always see "no tray" and quit for real on close, which
	// is safe, just not the tray-resident behavior.
	if ln, err := appipc.ListenTray(); err == nil {
		go serveLiveness(ln)
	} else {
		fmt.Println("agent-winglet-tray: liveness listener failed to start:", err)
	}

	go func() {
		for {
			select {
			case <-mOpen.ClickedCh:
				openDashboard()
			case <-mQuit.ClickedCh:
				quitDashboard()
				systray.Quit()
				return
			}
		}
	}()
}

func onExit() {
	appipc.CleanupTray()
}

// serveLiveness accepts and immediately closes connections for the lifetime
// of the tray — see the ListenTray call in onReady above.
func serveLiveness(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		conn.Close()
	}
}

// openDashboard asks a running dashboard to show its window; if none is
// reachable, it launches a fresh one instead (which binds its own IPC
// listener on startup, so a click right after this one would reach it).
func openDashboard() {
	if err := appipc.SendCommand(appipc.Show); err == nil {
		return
	}

	exe, err := appExecutablePath()
	if err != nil {
		fmt.Println("agent-winglet-tray: don't know how to launch the dashboard:", err)
		return
	}
	if err := exec.Command(exe).Start(); err != nil {
		fmt.Println("agent-winglet-tray: failed to launch the dashboard at", exe, "-", err)
	}
}

// quitDashboard tells a running dashboard to fully exit. If nothing answers,
// there's nothing to quit, which isn't an error worth reporting — a running
// dashboard is the exception, not the expected state, for a tray helper that
// lives at login independent of the dashboard's own lifecycle.
func quitDashboard() {
	_ = appipc.SendCommand(appipc.Quit)
}
