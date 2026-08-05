package appipc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSendCommandRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ln, err := Listen()
	if err != nil {
		t.Fatalf("Listen errored: %v", err)
	}
	defer ln.Close()
	defer Cleanup()

	received := make(chan Command, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		cmd, err := ReadCommand(conn)
		if err != nil {
			t.Errorf("ReadCommand errored: %v", err)
			return
		}
		received <- cmd
	}()

	if err := SendCommand(Show); err != nil {
		t.Fatalf("SendCommand errored: %v", err)
	}

	select {
	case cmd := <-received:
		if cmd != Show {
			t.Fatalf("received command = %q, want %q", cmd, Show)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for the server side to receive the command")
	}
}

func TestDialFailsWithNoPortFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, err := Dial(); err == nil {
		t.Fatal("Dial succeeded with no port file on disk, want an error")
	}
}

func TestDialFailsWithStalePortFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	p := filepath.Join(home, ".agent-winglet", "app.port")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	// A syntactically valid but unreachable port — nothing is listening on
	// it, so Dial should fail the same way it would for a dashboard that
	// crashed without cleaning up its port file.
	if err := os.WriteFile(p, []byte("1"), 0o600); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}

	if _, err := Dial(); err == nil {
		t.Fatal("Dial succeeded against a stale/unreachable port, want an error")
	}
}

func TestCleanupRemovesPortFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	ln, err := Listen()
	if err != nil {
		t.Fatalf("Listen errored: %v", err)
	}
	defer ln.Close()

	p, err := portFilePath()
	if err != nil {
		t.Fatalf("portFilePath errored: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("port file missing after Listen: %v", err)
	}

	Cleanup()

	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("port file still present after Cleanup: err = %v", err)
	}
}

