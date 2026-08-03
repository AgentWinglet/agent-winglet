package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeRegistry(t *testing.T, home string, dirs []string) {
	t.Helper()
	regDir := filepath.Join(home, ".agent-winglet")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	data, err := json.Marshal(dirs)
	if err != nil {
		t.Fatalf("marshal errored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "projects.json"), data, 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}
}

func TestLoadMissingRegistryReturnsEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	got, err := Load()
	if err != nil {
		t.Fatalf("Load errored: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no entries for a missing registry, got %v", got)
	}
}

func TestLoadSkipsMissingDirectories(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	existing := t.TempDir()
	gone := filepath.Join(home, "does-not-exist")
	writeRegistry(t, home, []string{existing, gone})

	got, err := Load()
	if err != nil {
		t.Fatalf("Load errored: %v", err)
	}
	if len(got) != 1 || got[0] != existing {
		t.Fatalf("expected only %q, got %v", existing, got)
	}
}

func TestHookInstalledTrueWhenCommandPresent(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	settings := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"/Users/x/go/bin/ledger-hook"}]}]}}`
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}

	if !HookInstalled(dir) {
		t.Fatalf("expected HookInstalled to be true")
	}
}

func TestHookInstalledFalseWhenMissing(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(`{"hooks":{}}`), 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}

	if HookInstalled(dir) {
		t.Fatalf("expected HookInstalled to be false")
	}
}

func TestHookInstalledFalseWhenSettingsFileMissing(t *testing.T) {
	dir := t.TempDir()
	if HookInstalled(dir) {
		t.Fatalf("expected HookInstalled to be false for a project with no .claude/settings.json")
	}
}

func TestLoadCorruptRegistryReturnsEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	regDir := filepath.Join(home, ".agent-winglet")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "projects.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}

	got, err := Load()
	if err != nil {
		t.Fatalf("Load errored: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no entries for a corrupt registry, got %v", got)
	}
}
