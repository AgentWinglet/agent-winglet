package statedir

import (
	"path/filepath"
	"testing"
)

func TestDirIsStableForSamePath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a, err := Dir("/some/project/path")
	if err != nil {
		t.Fatalf("Dir errored: %v", err)
	}
	b, err := Dir("/some/project/path")
	if err != nil {
		t.Fatalf("Dir errored: %v", err)
	}
	if a != b {
		t.Fatalf("Dir returned different paths for the same project path: %q vs %q", a, b)
	}
}

func TestDirDiffersForDifferentPaths(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a, err := Dir("/some/project/one")
	if err != nil {
		t.Fatalf("Dir errored: %v", err)
	}
	b, err := Dir("/some/project/two")
	if err != nil {
		t.Fatalf("Dir errored: %v", err)
	}
	if a == b {
		t.Fatalf("Dir returned the same path for two different projects: %q", a)
	}
}

func TestDirSanitizesBasename(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	d, err := Dir("/some/path/My Projëct!! 项目")
	if err != nil {
		t.Fatalf("Dir errored: %v", err)
	}
	base := filepath.Base(d)
	for _, r := range base {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			t.Fatalf("sanitized basename %q contains disallowed character %q", base, r)
		}
	}
}

func TestDirIsUnderGlobalProjectsRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	d, err := Dir("/some/project/path")
	if err != nil {
		t.Fatalf("Dir errored: %v", err)
	}
	want := filepath.Join(home, ".agent-winglet", "projects")
	if filepath.Dir(d) != want {
		t.Fatalf("Dir = %q, want a child of %q", d, want)
	}
}
