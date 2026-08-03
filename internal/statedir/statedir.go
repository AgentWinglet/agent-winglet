// Package statedir maps a project's identity path to its global,
// per-project state directory under ~/.agent-winglet/projects/. It is
// deliberately not git-aware — resolving what a project's identity path
// should be (e.g. its git root rather than the raw cwd) is
// internal/projectroot's job, kept separate so the two concerns stay
// independently testable.
package statedir

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
)

// maxBasenameLen caps the human-readable prefix purely for legibility of
// `ls ~/.agent-winglet/projects/`; the hash suffix is what actually
// guarantees uniqueness.
const maxBasenameLen = 40

// Dir returns the global per-project state directory for projectRoot,
// creating nothing. Callers MkdirAll as needed, same as they do today.
// projectRoot is expected to already be resolved (see internal/projectroot).
func Dir(projectRoot string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(projectRoot)
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(abs)

	sum := sha256.Sum256([]byte(clean))
	hash12 := hex.EncodeToString(sum[:])[:12]
	base := sanitize(filepath.Base(clean))

	return filepath.Join(home, ".agent-winglet", "projects", base+"-"+hash12), nil
}

// sanitize collapses anything outside [A-Za-z0-9_-] to '-' and caps the
// result at maxBasenameLen, so the human-readable prefix is always a safe,
// legible directory-name fragment regardless of what characters the
// project's actual basename contains.
func sanitize(name string) string {
	if name == "" {
		name = "project"
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	s := b.String()
	if len(s) > maxBasenameLen {
		s = s[:maxBasenameLen]
	}
	return s
}
