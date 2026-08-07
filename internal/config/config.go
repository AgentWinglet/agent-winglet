// Package config reads/writes the single global (not per-project) config
// file at ~/.agent-winglet/config.json. It holds two settings: Quiet, the
// config-file counterpart to AGENT_WINGLET_QUIET, and CompactNudgeDisabled.
// Quiet defaults to true (see Load) — the savings receipt is suppressed out
// of the box, and AGENT_WINGLET_QUIET=0 remains the way to opt back into it
// for a terminal session.
//
// The env var predates this file and exists because the original savings
// receipt only needed to be silenceable per-invocation. This file exists so
// the default can still be overridden by hand-editing
// ~/.agent-winglet/config.json ("quiet": false) even without an env var.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Config is the global agent-winglet config, shared across every project
// (unlike internal/ledger, internal/phase, internal/retire, and the session
// half of internal/stats, which are all keyed per-project and per-session).
type Config struct {
	Quiet bool `json:"quiet"`
	// CompactNudgeDisabled opts out of the /compact nudge (see
	// cmd/claude-hook's handlePhaseBoundary, cmd/codex-hook's
	// codexCompactNudgeOutput, and the dashboard settings toggle) — false by
	// default, unlike Quiet, since the nudge this gates is new and
	// opt-in-by-default, not a pre-existing behavior being made quieter.
	CompactNudgeDisabled bool `json:"compactNudgeDisabled"`
}

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-winglet", "config.json"), nil
}

// Load reads the global config, defaulting to Config{Quiet: true} if the
// file doesn't exist yet or is corrupt — same fallback behavior as
// internal/stats.loadJSON, except the fallback value is quiet-by-default
// rather than the zero value. An explicit "quiet": false in the file still
// overrides this default, since json.Unmarshal sets the field from the file
// when present.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	c := Config{Quiet: true}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return &c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return &Config{Quiet: true}, nil
	}
	return &c, nil
}

// Save writes the global config, creating ~/.agent-winglet if needed.
func Save(c *Config) error {
	p, err := path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}
