// Package stats tracks how much the other three mechanisms (ledger, phase's
// retirement path, and output budgeting) actually did, so a hook can surface
// a session-end receipt instead of leaving every substitution invisible.
//
// Two separate lifecycles, both keyed on projectDir:
//
//   - Session: same same-session-only lifecycle as ledger/phase/retire —
//     keyed additionally on session_id, wiped on SessionStart/PostCompact.
//     A receipt describing "this session's" activity is meaningless once the
//     session it describes is gone.
//   - Lifetime: a single file, not keyed by session_id and never wiped. This
//     doesn't violate the same-session-only constraint the other packages
//     enforce: that constraint exists because a stale *substitution* is
//     unsafe to replay across a session boundary. A plain count of how many
//     times a mechanism has fired carries no such risk — it's never replayed
//     into context, only displayed once as a number.
package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Session is the running tally of mechanism activity for one session.
type Session struct {
	DedupHits          int   `json:"dedupHits"`
	DedupBytes         int64 `json:"dedupBytes"`
	BudgetTrims        int   `json:"budgetTrims"`
	BudgetLinesOmitted int   `json:"budgetLinesOmitted"`
	RetiredCalls       int   `json:"retiredCalls"`
	RetiredBytes       int64 `json:"retiredBytes"`
}

// RecordDedup records one ledger repeat-hit that replaced would-be-replayed
// stdout of the given byte length.
func (s *Session) RecordDedup(bytes int) {
	s.DedupHits++
	s.DedupBytes += int64(bytes)
}

// RecordBudgetTrim records one output-budgeting trim that omitted the given
// number of lines.
func (s *Session) RecordBudgetTrim(linesOmitted int) {
	s.BudgetTrims++
	s.BudgetLinesOmitted += linesOmitted
}

// RecordRetire records one post-boundary investigate call whose output of
// the given byte length was archived instead of replayed.
func (s *Session) RecordRetire(bytes int) {
	s.RetiredCalls++
	s.RetiredBytes += int64(bytes)
}

// IsZero reports whether no mechanism fired this session.
func (s *Session) IsZero() bool {
	return *s == Session{}
}

// Lifetime is the running tally across every session, plus a count of how
// many sessions have contributed to it.
type Lifetime struct {
	Sessions           int   `json:"sessions"`
	DedupHits          int   `json:"dedupHits"`
	DedupBytes         int64 `json:"dedupBytes"`
	BudgetTrims        int   `json:"budgetTrims"`
	BudgetLinesOmitted int   `json:"budgetLinesOmitted"`
	RetiredCalls       int   `json:"retiredCalls"`
	RetiredBytes       int64 `json:"retiredBytes"`
}

// Add folds one session's tally into the lifetime tally and counts that
// session toward Sessions. Callers only call this for a session whose tally
// is non-zero (see the SessionEnd handler's zero-activity rule).
func (l *Lifetime) Add(s *Session) {
	l.Sessions++
	l.DedupHits += s.DedupHits
	l.DedupBytes += s.DedupBytes
	l.BudgetTrims += s.BudgetTrims
	l.BudgetLinesOmitted += s.BudgetLinesOmitted
	l.RetiredCalls += s.RetiredCalls
	l.RetiredBytes += s.RetiredBytes
}

func sessionPath(projectDir, sessionID string) string {
	return filepath.Join(projectDir, ".claude", "agent-winglet", sessionID+".stats.json")
}

func lifetimePath(projectDir string) string {
	return filepath.Join(projectDir, ".claude", "agent-winglet", "lifetime.stats.json")
}

func LoadSession(projectDir, sessionID string) (*Session, error) {
	var s Session
	if err := loadJSON(sessionPath(projectDir, sessionID), &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func SaveSession(projectDir, sessionID string, s *Session) error {
	return saveJSON(sessionPath(projectDir, sessionID), s)
}

// InvalidateSession deletes the session tally. Called on SessionStart and
// PostCompact, same as ledger.Invalidate/phase.Invalidate/retire.Invalidate,
// so a resumed or compacted session doesn't report a receipt describing
// activity from a part of the session that's now gone.
func InvalidateSession(projectDir, sessionID string) error {
	err := os.Remove(sessionPath(projectDir, sessionID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

func LoadLifetime(projectDir string) (*Lifetime, error) {
	var l Lifetime
	if err := loadJSON(lifetimePath(projectDir), &l); err != nil {
		return nil, err
	}
	return &l, nil
}

func SaveLifetime(projectDir string, l *Lifetime) error {
	return saveJSON(lifetimePath(projectDir), l)
}

func loadJSON(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		// Fall back to the zero value on a corrupt file, same as
		// ledger.Load/phase.Load.
		return nil
	}
	return nil
}

func saveJSON(path string, v interface{}) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
