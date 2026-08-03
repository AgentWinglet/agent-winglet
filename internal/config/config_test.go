package config

import (
	"os"
	"testing"
)

func TestLoadMissingFileReturnsZeroValue(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	c, err := Load()
	if err != nil {
		t.Fatalf("Load errored: %v", err)
	}
	if c.Quiet {
		t.Fatalf("expected Quiet=false for a missing config file, got %+v", c)
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if err := Save(&Config{Quiet: true}); err != nil {
		t.Fatalf("Save errored: %v", err)
	}
	c, err := Load()
	if err != nil {
		t.Fatalf("Load errored: %v", err)
	}
	if !c.Quiet {
		t.Fatalf("expected Quiet=true after Save, got %+v", c)
	}
}

func TestLoadCorruptFileReturnsZeroValue(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	if err := Save(&Config{Quiet: true}); err != nil {
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
	if c.Quiet {
		t.Fatalf("expected Quiet=false falling back from a corrupt file, got %+v", c)
	}
}
