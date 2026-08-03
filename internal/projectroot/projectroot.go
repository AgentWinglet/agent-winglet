// Package projectroot resolves a Claude Code session's raw cwd to a stable
// project identity: the nearest enclosing git root, or cwd itself if none is
// found. This is what collapses sessions started at a repo's root and at
// various subdirectories under it into a single project identity, instead of
// fragmenting one project into N state directories based on wherever a given
// session happened to start.
package projectroot

import (
	"os"
	"path/filepath"
)

// maxHops bounds the upward walk defensively; a real filesystem tree never
// gets remotely this deep, so hitting the cap only ever matters as a safety
// net against an unexpected loop.
const maxHops = 64

// Resolve walks upward from cwd looking for a .git entry (directory or
// file — a file means a worktree) and returns the directory that contains
// it. If none is found before the walk exhausts its bound, reaches the
// filesystem root, or would cross above the user's home directory, it
// returns cwd (absolute) unchanged — every caller gets a usable project
// identity either way.
//
// No network or subprocess calls: this runs on every tool call, so it has to
// stay a handful of os.Stats, not a `git rev-parse --show-toplevel` spawn.
func Resolve(cwd string) string {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return cwd
	}
	abs = filepath.Clean(abs)

	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	} else {
		home = filepath.Clean(home)
	}

	dir := abs
	for i := 0; i < maxHops; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		if dir == home {
			// Never search above the user's home directory — a stray .git
			// somewhere unexpected further up can't collapse unrelated
			// projects into one identity.
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return abs
}
