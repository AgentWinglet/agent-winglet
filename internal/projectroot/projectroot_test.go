package projectroot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveAtRepoRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir .git errored: %v", err)
	}

	if got := Resolve(root); got != root {
		t.Fatalf("Resolve(%q) = %q, want %q", root, got, root)
	}
}

func TestResolveSeveralLevelsUnderRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir .git errored: %v", err)
	}
	sub := filepath.Join(root, "cmd", "app", "frontend", "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}

	if got := Resolve(sub); got != root {
		t.Fatalf("Resolve(%q) = %q, want %q", sub, got, root)
	}
}

func TestResolveNoGitFallsBackToCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	dir := t.TempDir()
	want, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("Abs errored: %v", err)
	}
	if got := Resolve(dir); got != want {
		t.Fatalf("Resolve(%q) = %q, want %q (cwd unchanged)", dir, got, want)
	}
}

func TestResolveWorktreeResolvesToOwnRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	main := t.TempDir()
	if err := os.Mkdir(filepath.Join(main, ".git"), 0o755); err != nil {
		t.Fatalf("Mkdir main .git errored: %v", err)
	}

	worktree := t.TempDir()
	gitFile := filepath.Join(worktree, ".git")
	content := "gitdir: " + filepath.Join(main, ".git", "worktrees", "wt") + "\n"
	if err := os.WriteFile(gitFile, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile .git errored: %v", err)
	}

	if got := Resolve(worktree); got != worktree {
		t.Fatalf("Resolve(%q) = %q, want %q (worktree's own root, not main's)", worktree, got, worktree)
	}
}
