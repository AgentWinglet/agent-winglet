// Package registry reads the global project registry at
// ~/.agent-winglet/projects.json — a flat JSON array of absolute project
// paths, written by install.sh (see its "Register this project" step).
//
// This package is read-only. install.sh owns writing the file, including
// deduping on install and lazily pruning entries whose directory no longer
// exists on its next run — see install.sh's comments. A project directory
// that's been moved or deleted since the registry was last written is
// skipped silently here rather than erroring, since a stale registry entry
// is a cold-path edge case, not something worth surfacing as a failure.
package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
)

func path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".agent-winglet", "projects.json"), nil
}

// Load returns every registered project directory that still exists on
// disk. A missing registry file (hook never installed anywhere yet) is not
// an error — it returns an empty slice, same as ledger/phase/retire/stats
// treat a missing state file as the zero value rather than an error.
func Load() ([]string, error) {
	p, err := path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var all []string
	if err := json.Unmarshal(data, &all); err != nil {
		// Corrupt registry: fail soft, same fallback the other internal/*
		// packages use for a corrupt state file.
		return nil, nil
	}

	var existing []string
	for _, dir := range all {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		existing = append(existing, dir)
	}
	return existing, nil
}

// HookInstalled reports whether projectDir's .claude/settings.json has a
// hook command referencing the ledger-hook binary. Registry presence alone
// isn't enough — a project can be removed from the hook (settings.json
// edited or hook config stripped) without being removed from this registry,
// since only install.sh writes/prunes the registry, not uninstallation.
func HookInstalled(projectDir string) bool {
	data, err := os.ReadFile(filepath.Join(projectDir, ".claude", "settings.json"))
	if err != nil {
		return false
	}
	var v interface{}
	if err := json.Unmarshal(data, &v); err != nil {
		return false
	}
	return containsLedgerHookCommand(v)
}

// containsLedgerHookCommand walks an arbitrary decoded-JSON value looking
// for a "command" field whose value's basename is "ledger-hook" — matching
// install.sh's hook config shape without depending on the exact absolute
// GOBIN path, which varies per machine.
func containsLedgerHookCommand(v interface{}) bool {
	switch node := v.(type) {
	case map[string]interface{}:
		if cmd, ok := node["command"].(string); ok && filepath.Base(cmd) == "ledger-hook" {
			return true
		}
		for _, child := range node {
			if containsLedgerHookCommand(child) {
				return true
			}
		}
	case []interface{}:
		for _, child := range node {
			if containsLedgerHookCommand(child) {
				return true
			}
		}
	}
	return false
}
