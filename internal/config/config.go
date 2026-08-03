// Package config reads/writes the single global (not per-project) config
// file at ~/.agent-winglet/config.json. Today it holds exactly one setting:
// Quiet, the config-file counterpart to AGENT_WINGLET_QUIET.
//
// The env var exists because the original savings receipt only needed to be
// silenceable per-invocation. A GUI toggle can't set an env var for a
// terminal-launched Claude Code session — different process tree, no shared
// env — so it needs a file both the hook binary and the app can read/write.
// The env var still takes precedence when set, for backward compatibility:
// see Quiet's doc comment.
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
}

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-winglet", "config.json"), nil
}

// Load reads the global config, returning a zero-value Config (Quiet: false)
// if the file doesn't exist yet or is corrupt — same fallback behavior as
// internal/stats.loadJSON.
func Load() (*Config, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	var c Config
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return &c, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return &Config{}, nil
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
