// Package appipc is the minimal local control channel between the Winglet
// dashboard (cmd/agent-winglet-app) and its tray helper (cmd/agent-winglet-tray)
// — two separate processes, since bundling a systray library into the same
// binary as Wails fails to link on macOS (see cmd/agent-winglet-app/main.go's
// doc comment). The channel carries exactly two commands, "show the window"
// and "quit the app", never any data, so a loopback TCP port advertised via a
// small file under ~/.agent-winglet is enough — no need for OS-specific Unix
// sockets/named pipes for a channel this narrow.
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
	// Quit asks the dashboard to actually exit, bypassing the hide-on-close
	// behavior its OnBeforeClose otherwise applies — this is the only path
	// that fully quits the dashboard once a tray helper is managing it.
	Quit Command = "QUIT"

	ack = "OK"

	dialTimeout = 500 * time.Millisecond
	ioTimeout   = 2 * time.Second
)

func portFilePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-winglet", "app.port"), nil
}

// Listen binds a loopback TCP listener on an OS-assigned port and publishes
// that port to the port file so Dial can find it. Callers should Cleanup on
// shutdown — a stale file left pointing at a dead port isn't unsafe (Dial and
// SendCommand both treat an unreachable port as "not running," not an
// error), just untidy.
func Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}

	p, err := portFilePath()
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

// Cleanup removes the port file. Safe to call even if Listen was never
// called, or the file is already gone.
func Cleanup() {
	p, err := portFilePath()
	if err != nil {
		return
	}
	os.Remove(p)
}

// Dial connects to the dashboard's IPC listener. An error here — missing or
// unreadable port file, or nothing answering on the recorded port — means
// "the dashboard isn't running," not a real failure; callers should treat it
// that way rather than surfacing it.
func Dial() (net.Conn, error) {
	p, err := portFilePath()
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

// SendCommand dials the dashboard, sends cmd, and waits for its
// acknowledgement.
func SendCommand(cmd Command) error {
	conn, err := Dial()
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
