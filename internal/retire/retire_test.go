package retire

import (
	"os"
	"testing"
)

func TestStoreWritesRetrievableContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	path, err := Store(dir, "sess1", []byte("hello world"))
	if err != nil {
		t.Fatalf("Store errored: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading stored content errored: %v", err)
	}
	if string(got) != "hello world" {
		t.Fatalf("stored content = %q, want %q", got, "hello world")
	}
}

func TestStoreIsIdempotentForIdenticalContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	first, err := Store(dir, "sess1", []byte("same"))
	if err != nil {
		t.Fatalf("first Store errored: %v", err)
	}
	second, err := Store(dir, "sess1", []byte("same"))
	if err != nil {
		t.Fatalf("second Store errored: %v", err)
	}
	if first != second {
		t.Fatalf("identical content produced different paths: %q vs %q", first, second)
	}
}

func TestStoreDifferentContentDifferentPath(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	first, err := Store(dir, "sess1", []byte("a"))
	if err != nil {
		t.Fatalf("first Store errored: %v", err)
	}
	second, err := Store(dir, "sess1", []byte("b"))
	if err != nil {
		t.Fatalf("second Store errored: %v", err)
	}
	if first == second {
		t.Fatalf("different content produced the same path: %q", first)
	}
}

func TestInvalidateRemovesStoredContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	path, err := Store(dir, "sess1", []byte("x"))
	if err != nil {
		t.Fatalf("Store errored: %v", err)
	}
	if err := Invalidate(dir, "sess1"); err != nil {
		t.Fatalf("Invalidate errored: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("retired content survived Invalidate: err=%v", err)
	}
}

func TestInvalidateOnMissingDirIsNoop(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if err := Invalidate(dir, "sess-never-seen"); err != nil {
		t.Fatalf("Invalidate on missing dir errored: %v", err)
	}
}

func TestSessionsAreIsolated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if _, err := Store(dir, "sess1", []byte("x")); err != nil {
		t.Fatalf("Store errored: %v", err)
	}
	if err := Invalidate(dir, "sess2"); err != nil {
		t.Fatalf("Invalidate errored: %v", err)
	}
	// sess1's content must still exist after invalidating a different session.
	path, err := Store(dir, "sess1", []byte("x"))
	if err != nil {
		t.Fatalf("re-Store errored: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("sess1 content missing after unrelated session's Invalidate: %v", err)
	}
}
