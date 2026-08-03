// Package registry reads and writes the global project registry at
// ~/.agent-winglet/projects.json — a flat JSON array of absolute project
// paths.
//
// Now that the hook installs globally (into ~/.claude/settings.json, see
// install.sh) rather than per-project, there's no longer an install.sh
// invocation inside each project to register it at. Instead, cmd/ledger-hook
// calls Register itself on SessionStart/PostCompact — the first time the
// hook fires for a given project's cwd, that project lands in the registry.
// Register dedupes and prunes stale entries (directories moved/deleted since
// last written) on every call, the same behavior install.sh used to provide.
// A project directory that's been moved or deleted since the registry was
// last written is skipped silently by Load rather than erroring, since a
// stale registry entry is a cold-path edge case, not something worth
// surfacing as a failure.
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

// Register adds dir to the registry if it isn't already present, first
// pruning any entries whose directory no longer exists on disk — the same
// dedupe-and-prune behavior install.sh used to perform at install time, now
// performed on every call since there's no longer a single install-time
// moment to do it at. Safe to call on every session start: a no-op write
// (dir already present, nothing stale to prune) still costs a read + write,
// which is cheap enough at hook-invocation frequency.
func Register(dir string) error {
	p, err := path()
	if err != nil {
		return err
	}

	var all []string
	data, err := os.ReadFile(p)
	switch {
	case os.IsNotExist(err):
		// No registry yet: this is the first project the hook has ever
		// fired in on this machine.
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &all); err != nil {
			// Corrupt registry: start fresh, same fail-soft fallback Load uses.
			all = nil
		}
	}

	pruned := make([]string, 0, len(all)+1)
	found := false
	for _, existing := range all {
		info, statErr := os.Stat(existing)
		if statErr != nil || !info.IsDir() {
			continue
		}
		if existing == dir {
			found = true
		}
		pruned = append(pruned, existing)
	}
	if !found {
		pruned = append(pruned, dir)
	}

	out, err := json.Marshal(pruned)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// HookInstalled reports whether projectDir's own .claude/settings.json has a
// hook command referencing the ledger-hook binary — a project-level install,
// as install.sh wrote before the hook became global. Registry presence alone
// isn't enough — a project can be removed from the hook (settings.json
// edited or hook config stripped) without being removed from this registry,
// since Register only ever adds/prunes-by-existence, never removes an entry
// because its hook config was edited.
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

// GlobalHookInstalled reports whether the global ~/.claude/settings.json —
// install.sh's default install target — has a hook command referencing the
// ledger-hook binary. Since the hook is global by default, this is true for
// effectively every registered project unless the user has since removed it.
func GlobalHookInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	data, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
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
