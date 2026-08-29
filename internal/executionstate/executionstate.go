// Package executionstate keeps compact operational state derived from
// context that Winglet is already retiring.
//
// It deliberately does not archive raw output. Raw evidence stays in
// internal/retire's disk-backed store, and state entries refer back to that
// existing retired-content path.
package executionstate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/AgentWinglet/agent-winglet/internal/statedir"
)

const (
	version        = 1
	maxItems       = 40
	maxItemTextLen = 220
	maxPatchItems  = 6
)

var pathRe = regexp.MustCompile(`(?:^|[\s"'(:])((?:\.{1,2}/|/)?[A-Za-z0-9_.@+~=-]+(?:/[A-Za-z0-9_.@+~=-]+)+(?:\.[A-Za-z0-9_+~=-]+)?)`)

// Item is one compact fact or operational clue retained after its source
// output has been retired.
type Item struct {
	Text       string   `json:"text"`
	Kind       string   `json:"kind,omitempty"`
	SourceRefs []string `json:"source_refs,omitempty"`
	UpdatedAt  string   `json:"updated_at,omitempty"`
}

// State is the session-scoped execution state injected through retire
// receipts. It is compact by construction; raw evidence lives in retire.
type State struct {
	Version   int    `json:"version"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Items     []Item `json:"items,omitempty"`
}

// RetirableContextItem describes output that an existing retirement path has
// already found eligible to remove from active context.
type RetirableContextItem struct {
	Label      string
	Key        string
	Content    string
	SourceRef  string
	Successful bool
}

func statePath(projectDir, sessionID string) (string, error) {
	d, err := statedir.Dir(projectDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, sessionID+".execution-state.json"), nil
}

// Load returns the current session execution state, or an empty state if no
// state has been projected yet.
func Load(projectDir, sessionID string) (*State, error) {
	p, err := statePath(projectDir, sessionID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if os.IsNotExist(err) {
		return &State{Version: version}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return &State{Version: version}, nil
	}
	if s.Version == 0 {
		s.Version = version
	}
	return &s, nil
}

// Save validates and atomically stores a compact state file.
func Save(projectDir, sessionID string, s *State) error {
	validate(s)
	p, err := statePath(projectDir, sessionID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Invalidate deletes the projected state for a session. It follows the same
// lifecycle as phase/ledger/retire operational state.
func Invalidate(projectDir, sessionID string) error {
	p, err := statePath(projectDir, sessionID)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Project applies a validated patch derived from a retirable item. The
// returned items are the entries added or refreshed by this call.
func Project(projectDir, sessionID string, item RetirableContextItem) ([]Item, int, error) {
	patch := BuildPatch(item)
	if len(patch) == 0 {
		return nil, 0, nil
	}
	st, err := Load(projectDir, sessionID)
	if err != nil {
		return nil, 0, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	st.Version = version
	st.UpdatedAt = now

	for i := range patch {
		patch[i].Text = cleanText(patch[i].Text)
		patch[i].Kind = cleanText(patch[i].Kind)
		patch[i].SourceRefs = cleanRefs(patch[i].SourceRefs)
		patch[i].UpdatedAt = now
		upsert(st, patch[i])
	}
	validate(st)
	if err := Save(projectDir, sessionID, st); err != nil {
		return nil, 0, err
	}
	return patch, SerializedLen(st), nil
}

// BuildPatch extracts compact durable information from an eligible retired
// context item. It is intentionally conservative; unknown content produces no
// patch rather than a prose summary of raw output.
func BuildPatch(item RetirableContextItem) []Item {
	sourceRefs := cleanRefs([]string{item.SourceRef})
	if len(sourceRefs) == 0 {
		return nil
	}

	var out []Item
	key := cleanText(item.Key)
	paths := extractPaths(key + "\n" + item.Content)
	if len(paths) > 0 {
		text := "Relevant paths from retired " + label(item.Label) + ": " + strings.Join(paths, ", ")
		out = append(out, Item{Text: text, Kind: "paths", SourceRefs: sourceRefs})
	}
	if item.Successful && looksLikeTestCommand(key) && looksLikePassingTestOutput(item.Content) {
		out = append(out, Item{
			Text:       "Latest retired test command passed: " + trimLen(key, 160),
			Kind:       "test-status",
			SourceRefs: sourceRefs,
		})
	}
	if len(out) > maxPatchItems {
		out = out[:maxPatchItems]
	}
	return out
}

// Format renders a compact state block suitable for a retirement receipt.
func Format(items []Item) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Relevant execution state retained:")
	for _, item := range items {
		if item.Text == "" {
			continue
		}
		b.WriteString("\n- ")
		b.WriteString(item.Text)
	}
	return b.String()
}

// SerializedLen returns the size of the compact state representation.
func SerializedLen(s *State) int {
	validate(s)
	data, err := json.Marshal(s)
	if err != nil {
		return 0
	}
	return len(data)
}

func upsert(s *State, item Item) {
	for i := range s.Items {
		if s.Items[i].Text == item.Text {
			s.Items[i] = mergeItem(s.Items[i], item)
			return
		}
	}
	s.Items = append(s.Items, item)
}

func mergeItem(old, next Item) Item {
	old.Kind = next.Kind
	old.UpdatedAt = next.UpdatedAt
	old.SourceRefs = cleanRefs(append(old.SourceRefs, next.SourceRefs...))
	return old
}

func validate(s *State) {
	if s == nil {
		return
	}
	s.Version = version
	if len(s.Items) > maxItems {
		s.Items = s.Items[len(s.Items)-maxItems:]
	}
	for i := range s.Items {
		s.Items[i].Text = trimLen(cleanText(s.Items[i].Text), maxItemTextLen)
		s.Items[i].Kind = trimLen(cleanText(s.Items[i].Kind), 40)
		s.Items[i].SourceRefs = cleanRefs(s.Items[i].SourceRefs)
	}
}

func extractPaths(text string) []string {
	matches := pathRe.FindAllStringSubmatch(text, -1)
	seen := map[string]bool{}
	var paths []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		p := strings.Trim(m[1], ".,:;)")
		if !plausiblePath(p) || seen[p] {
			continue
		}
		seen[p] = true
		paths = append(paths, p)
		if len(paths) == 5 {
			break
		}
	}
	sort.Strings(paths)
	return paths
}

func plausiblePath(p string) bool {
	if p == "" || strings.Contains(p, "://") {
		return false
	}
	if strings.Contains(p, "/.agent-winglet/") {
		return false
	}
	base := filepath.Base(p)
	return strings.Contains(base, ".") || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../")
}

func looksLikeTestCommand(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, " test") ||
		strings.Contains(k, "pytest") ||
		strings.Contains(k, "go test") ||
		strings.Contains(k, "cargo test") ||
		strings.Contains(k, "npm test") ||
		strings.Contains(k, "pnpm test") ||
		strings.Contains(k, "yarn test")
}

func looksLikePassingTestOutput(out string) bool {
	lower := strings.ToLower(out)
	return strings.Contains(lower, "pass") ||
		strings.Contains(lower, "passed") ||
		strings.Contains(lower, "ok ") ||
		strings.Contains(lower, "tests passed")
}

func cleanRefs(refs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, ref := range refs {
		ref = cleanText(ref)
		if ref == "" || seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func label(s string) string {
	s = cleanText(s)
	if s == "" {
		return "context"
	}
	return s
}

func trimLen(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.TrimSpace(s[:n-3]) + "..."
}
