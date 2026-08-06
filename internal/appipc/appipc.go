// Package appipc is the minimal local control channel between the Winglet
// dashboard (cmd/agent-winglet-app) and its tray helper (cmd/agent-winglet-tray)
// — two separate processes, since bundling a systray library into the same
// binary as Wails fails to link on macOS (see cmd/agent-winglet-app/main.go's
// doc comment). Two loopback TCP listeners, one per process, each advertised
// via its own small port file under ~/.agent-winglet, carry the same tiny
// Command vocabulary in both directions — no need for OS-specific Unix
// sockets/named pipes for a channel this narrow.
//
// The tray dials the dashboard (SendCommand/Dial/Listen, app.port) to ask it
// to show its window or quit — see app.go's handleIPCConn. The dashboard
// dials the tray (SendTrayCommand/ListenTray, tray.port) for two things: a
// bare connect-and-close as a liveness probe (TrayRunning, used at startup —
// see app.go's ensureTrayRunning — to decide whether a tray helper needs
// launching), and a real Quit so the dashboard's own Quit button can fully
// tear down the tray too, not just itself — see cmd/agent-winglet-tray's
// handleControlConn.
package appipc

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Command is one of the commands the dashboard understands over the IPC
// channel.
type Command string

const (
	// Show asks the dashboard to bring its window to front.
	Show Command = "SHOW"
	// Quit asks the dashboard to actually exit.
	Quit Command = "QUIT"

	ack = "OK"

	dialTimeout = 500 * time.Millisecond
	ioTimeout   = 2 * time.Second
)

// appPortFile carries the dashboard's two-command channel (Show/Quit),
// dialed by the tray. trayPortFile carries nothing but its own existence —
// dialed by the dashboard, purely to answer TrayRunning.
const (
	appPortFile  = "app.port"
	trayPortFile = "tray.port"
)

func portFilePath(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-winglet", name), nil
}

// listen binds a loopback TCP listener on an OS-assigned port and publishes
// that port to the named port file so dial can find it. Callers should clean
// up on shutdown — a stale file left pointing at a dead port isn't unsafe
// (dial treats an unreachable port as "not running," not an error), just
// untidy.
func listen(name string) (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	p, err := portFilePath(name)
	if err != nil {
		ln.Close()
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		ln.Close()
		return nil, err
	}

	port := ln.Addr().(*net.TCPAddr).Port
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(port)), 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	if err := os.Rename(tmp, p); err != nil {
		ln.Close()
		return nil, err
	}

	return ln, nil
}

// cleanup removes the named port file. Safe to call even if listen was never
// called, or the file is already gone.
func cleanup(name string) {
	p, err := portFilePath(name)
	if err != nil {
		return
	}
	os.Remove(p)
}

// dial connects to whatever's listening at the named port file. An error
// here — missing or unreadable port file, or nothing answering on the
// recorded port — means "that side isn't running," not a real failure;
// callers should treat it that way rather than surfacing it.
func dial(name string) (net.Conn, error) {
	p, err := portFilePath(name)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	port := strings.TrimSpace(string(data))
	if _, err := strconv.Atoi(port); err != nil {
		return nil, fmt.Errorf("appipc: invalid port file contents %q", port)
	}
	return net.DialTimeout("tcp", "127.0.0.1:"+port, dialTimeout)
}

// Listen binds the dashboard's IPC listener. See listen.
func Listen() (net.Listener, error) { return listen(appPortFile) }

// Cleanup removes the dashboard's port file. See cleanup.
func Cleanup() { cleanup(appPortFile) }

// Dial connects to the dashboard's IPC listener. See dial.
func Dial() (net.Conn, error) { return dial(appPortFile) }

// ListenTray binds the tray helper's control listener. Most accepted
// connections are bare liveness probes from TrayRunning (dial-and-close, no
// command sent — ReadCommand on those just errors out immediately, which
// is fine, there's nothing to do); SendTrayCommand's Quit is the one real
// command this side needs to handle. See listen.
func ListenTray() (net.Listener, error) { return listen(trayPortFile) }

// CleanupTray removes the tray's port file. See cleanup.
func CleanupTray() { cleanup(trayPortFile) }

// TrayRunning reports whether a tray helper is currently reachable. The
// dashboard uses this to decide whether hiding on close is safe — hiding
// only makes sense if a tray is actually still around to bring the window
// back; see app.go's beforeClose.
func TrayRunning() bool {
	conn, err := dial(trayPortFile)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// sendCommand dials whatever's listening at the named port file, sends cmd,
// and waits for its acknowledgement.
func sendCommand(name string, cmd Command) error {
	conn, err := dial(name)
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(ioTimeout))

	if _, err := fmt.Fprintf(conn, "%s\n", cmd); err != nil {
		return err
	}

	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return err
	}
	if strings.TrimSpace(reply) != ack {
		return fmt.Errorf("appipc: unexpected reply %q", reply)
	}
	return nil
}

// SendCommand dials the dashboard, sends cmd, and waits for its
// acknowledgement. See sendCommand.
func SendCommand(cmd Command) error { return sendCommand(appPortFile, cmd) }

// SendTrayCommand dials the tray helper, sends cmd, and waits for its
// acknowledgement. See sendCommand.
func SendTrayCommand(cmd Command) error { return sendCommand(trayPortFile, cmd) }

// ReadCommand reads one command off an accepted connection and acknowledges
// it immediately (the ack confirms receipt, not that the dashboard has
// finished acting on it — the two commands here are fire-and-forget from the
// tray's point of view). Used by the dashboard's accept loop.
func ReadCommand(conn net.Conn) (Command, error) {
	conn.SetDeadline(time.Now().Add(ioTimeout))

	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}
	cmd := Command(strings.TrimSpace(line))

	if _, err := fmt.Fprintf(conn, "%s\n", ack); err != nil {
		return "", err
	}
	return cmd, nil
}
