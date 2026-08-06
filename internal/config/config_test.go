package config

import (
	"os"
	"testing"
)

func TestLoadMissingFileDefaultsToQuiet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatalf("Load errored: %v", err)
	}
	if !c.Quiet {
		t.Fatalf("expected Quiet=true for a missing config file, got %+v", c)
	}
}

func TestSaveThenLoadRoundTripsExplicitFalse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := Save(&Config{Quiet: false}); err != nil {
		t.Fatalf("Save errored: %v", err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load errored: %v", err)
	}
	if c.Quiet {
		t.Fatalf("expected Quiet=false after Save to override the default, got %+v", c)
	}
}

func TestSaveThenLoadRoundTripsCompactNudgeDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := Save(&Config{CompactNudgeDisabled: true}); err != nil {
		t.Fatalf("Save errored: %v", err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load errored: %v", err)
	}
	if !c.CompactNudgeDisabled {
		t.Fatalf("expected CompactNudgeDisabled=true after Save, got %+v", c)
	}
}

func TestLoadCorruptFileDefaultsToQuiet(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := Save(&Config{Quiet: false}); err != nil {
		t.Fatalf("seeding Save errored: %v", err)
	}
	p, err := path()
	if err != nil {
		t.Fatalf("path errored: %v", err)
	}
	if err := os.WriteFile(p, []byte("not json"), 0o644); err != nil {
		t.Fatalf("writing corrupt config errored: %v", err)
	}

	c, err := Load()
	if err != nil {
		t.Fatalf("Load errored: %v", err)
	}
	if !c.Quiet {
		t.Fatalf("expected Quiet=true falling back from a corrupt file, got %+v", c)
	}
}
