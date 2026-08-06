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
// dashboard to actually exit and then exits itself. The dashboard's own Quit
// button (App.QuitApp) is the mirror image of that last part: it sends this
// tray a Quit command of its own (handleControlConn below), so quitting from
// either side tears down both, not just the half you clicked. Closing the
// dashboard's window on its own (titlebar button, Cmd+Q/Alt+F4, Dock Quit)
// leaves this tray running — its "Open Winglet" relaunches the dashboard on
// demand, just like it would if the dashboard had never been started this
// session (openDashboard here, App.ensureTrayRunning on the dashboard's
// side).
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

	// serveControl lets the dashboard tell whether a tray is already running
	// before launching one at startup (appipc.TrayRunning, see App.
	// ensureTrayRunning), and lets the dashboard's own Quit button
	// (App.QuitApp) fully exit this tray too, not just itself. A failure to
	// bind here (e.g. two tray instances racing) just means the dashboard's
	// Quit button has nothing to reach — the tray itself still works, it
	// just won't hear a dashboard-initiated Quit.
	if ln, err := appipc.ListenTray(); err == nil {
		go serveControl(ln)
	} else {
		fmt.Println("agent-winglet-tray: control listener failed to start:", err)
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

// serveControl accepts connections for the lifetime of the tray — see the
// ListenTray call in onReady above.
func serveControl(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go handleControlConn(conn)
	}
}

// handleControlConn reads one command off an accepted connection, same
// protocol as the dashboard's own handleIPCConn. A bare liveness probe from
// TrayRunning sends nothing and closes right away, so ReadCommand just
// errors out here with nothing to do — expected, not logged. Quit is the
// one command this side acts on, by exiting exactly the way the tray's own
// Quit menu item does.
func handleControlConn(conn net.Conn) {
	defer conn.Close()

	cmd, payload, err := appipc.ReadCommand(conn)
	if err != nil {
		return
	}
	switch cmd {
	case appipc.Quit:
		systray.Quit()
	case appipc.Notify:
		showToast(payload)
	}
}

// showToast launches the dashboard binary in its ephemeral --toast mode
// (cmd/agent-winglet-app's runToast) with payload passed straight through —
// the tray forwards it byte-for-byte from internal/appipc's Notify command,
// it doesn't parse it itself. Best-effort, like openDashboard: a failure
// here (binary not installed at the expected path, exec error) just means
// this one nudge doesn't get a toast, not a broken tray.
func showToast(payload string) {
	exe, err := appExecutablePath()
	if err != nil {
		fmt.Println("agent-winglet-tray: don't know how to launch the dashboard for a toast:", err)
		return
	}
	if err := exec.Command(exe, "--toast", payload).Start(); err != nil {
		fmt.Println("agent-winglet-tray: failed to launch toast at", exe, "-", err)
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
