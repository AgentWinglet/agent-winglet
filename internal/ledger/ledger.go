// Package ledger implements the Session Ledger: a same-session-only record of
// content hashes already sent to the model, used to substitute a compact
// "unchanged since turn N" reference for exact-repeat tool output.
//
// Hard constraint: never valid across a session restart or compaction. This is
// enforced by keying the state file on session_id and deleting it outright on
// SessionStart/PostCompact, rather than by any time- or content-based heuristic.
package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

type Entry struct {
	Hash string `json:"hash"`
	Turn int    `json:"turn"`
}

type State struct {
	Turn    int              `json:"turn"`
	Entries map[string]Entry `json:"entries"`
}

func statePath(projectDir, sessionID string) string {
	return filepath.Join(projectDir, ".claude", "agent-winglet", sessionID+".json")
}

func Load(projectDir, sessionID string) (*State, error) {
	data, err := os.ReadFile(statePath(projectDir, sessionID))
	if os.IsNotExist(err) {
		return &State{Entries: map[string]Entry{}}, nil
	}
	if err != nil {
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return &State{Entries: map[string]Entry{}}, nil
	}
	if s.Entries == nil {
		s.Entries = map[string]Entry{}
	}
	return &s, nil
}

func Save(projectDir, sessionID string, s *State) error {
	p := statePath(projectDir, sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// Invalidate deletes the ledger for a session. Called on SessionStart and PostCompact
// so a resumed or compacted session never treats a prior state file as still valid.
func Invalidate(projectDir, sessionID string) error {
	err := os.Remove(statePath(projectDir, sessionID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func Hash(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Check records this turn's observation of key/content and reports whether identical
// content was already recorded earlier in the same session. On a repeat it returns the
// turn number that first saw the content; on first sight it advances the turn counter
// and stores the new hash.
func (s *State) Check(key, content string) (repeatOfTurn int, isRepeat bool) {
	h := Hash(content)
	if e, ok := s.Entries[key]; ok && e.Hash == h {
		return e.Turn, true
	}
	s.Turn++
	s.Entries[key] = Entry{Hash: h, Turn: s.Turn}
	return 0, false
}
