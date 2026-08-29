package executionstate

import (
	"os"
	"strings"
	"testing"
)

func TestProjectStoresCompactStateWithExistingArchiveRef(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	source := "/tmp/winglet-retired/source.txt"

	items, bytes, err := Project(dir, "sess1", RetirableContextItem{
		Label:      "investigation",
		Key:        "Grep {\"path\":\"src/lib/firebase.ts\"}",
		Content:    "src/lib/firebase.ts\nsrc/app/auth/login.tsx\n",
		SourceRef:  source,
		Successful: true,
	})
	if err != nil {
		t.Fatalf("Project errored: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("projected %d items, want 1", len(items))
	}
	if !strings.Contains(items[0].Text, "src/lib/firebase.ts") {
		t.Fatalf("projected item missing path: %+v", items[0])
	}
	if len(items[0].SourceRefs) != 1 || items[0].SourceRefs[0] != source {
		t.Fatalf("source refs = %+v, want existing archive path", items[0].SourceRefs)
	}
	if bytes == 0 {
		t.Fatalf("Project reported zero state bytes")
	}

	p, err := statePath(dir, "sess1")
	if err != nil {
		t.Fatalf("statePath errored: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	if strings.Contains(string(data), "Full raw output") {
		t.Fatalf("state stored raw output: %s", data)
	}
	if !strings.Contains(string(data), source) {
		t.Fatalf("state missing archive source ref: %s", data)
	}
}

func TestProjectIgnoresUnknownContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	items, bytes, err := Project(dir, "sess1", RetirableContextItem{
		Label:     "investigation",
		Key:       "opaque",
		Content:   "nothing durable here",
		SourceRef: "/tmp/source.txt",
	})
	if err != nil {
		t.Fatalf("Project errored: %v", err)
	}
	if len(items) != 0 || bytes != 0 {
		t.Fatalf("Project = (%+v, %d), want no patch", items, bytes)
	}
}

func TestFormatRendersOnlyProjectedItems(t *testing.T) {
	got := Format([]Item{{Text: "Authentication uses Firebase", SourceRefs: []string{"/tmp/source.txt"}}})
	if !strings.Contains(got, "Relevant execution state retained:") ||
		!strings.Contains(got, "- Authentication uses Firebase") ||
		strings.Contains(got, "/tmp/source.txt") {
		t.Fatalf("Format output = %q", got)
	}
}
