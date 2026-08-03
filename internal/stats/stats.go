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

	"github.com/umitkaanusta/agent-winglet/internal/transcript"
)

// Session is the running tally of mechanism activity for one session.
type Session struct {
	DedupHits          int   `json:"dedupHits"`
	DedupBytes         int64 `json:"dedupBytes"`
	BudgetTrims        int   `json:"budgetTrims"`
	BudgetLinesOmitted int   `json:"budgetLinesOmitted"`
	BudgetBytesOmitted int64 `json:"budgetBytesOmitted"`
	RetiredCalls       int   `json:"retiredCalls"`
	RetiredBytes       int64 `json:"retiredBytes"`
	// ProcessedBytes is the original byte length of every tool response the
	// hook actually evaluated for suppression — the denominator for
	// Percent, incremented whether or not the call ended up suppressed.
	// Deliberately excluded from IsZero/mechanism-activity checks: a
	// session that only ever passed output through untouched still grows
	// this counter, and that alone isn't "a mechanism fired."
	ProcessedBytes int64 `json:"processedBytes"`
	// TranscriptTokens, TranscriptCostUSD, and TranscriptContentBytes carry
	// this session's real transcript-derived usage (see internal/transcript)
	// — the input-side token total, its priced cost at real per-token
	// rates, and the raw content-byte size those tokens represent. Same
	// treatment as ProcessedBytes: real usage data existing isn't "a
	// mechanism firing," so these are excluded from IsZero.
	TranscriptTokens       int64   `json:"transcriptTokens"`
	TranscriptCostUSD      float64 `json:"transcriptCostUsd"`
	TranscriptContentBytes int64   `json:"transcriptContentBytes"`
}

// RecordDedup records one ledger repeat-hit that replaced would-be-replayed
// stdout of the given byte length.
func (s *Session) RecordDedup(bytes int) {
	s.DedupHits++
	s.DedupBytes += int64(bytes)
}

// RecordBudgetTrim records one output-budgeting trim that omitted the given
// number of lines, totaling bytesOmitted bytes — on the same byte unit as
// RecordDedup/RecordRetire so all three can share one denominator (see
// Percent).
func (s *Session) RecordBudgetTrim(linesOmitted int, bytesOmitted int64) {
	s.BudgetTrims++
	s.BudgetLinesOmitted += linesOmitted
	s.BudgetBytesOmitted += bytesOmitted
}

// RecordRetire records one post-boundary investigate call whose output of
// the given byte length was archived instead of replayed.
func (s *Session) RecordRetire(bytes int) {
	s.RetiredCalls++
	s.RetiredBytes += int64(bytes)
}

// RecordProcessed records the original byte length of a tool response the
// hook evaluated for suppression, regardless of the outcome (suppressed or
// passed through untouched). Called once per call handlePostToolUse/
// handleRetireInvestigate actually looks at.
func (s *Session) RecordProcessed(bytes int) {
	s.ProcessedBytes += int64(bytes)
}

// SetTranscriptUsage copies a transcript read (see internal/transcript) onto
// the session. Called once per SessionEnd, after the transcript file has
// already been fully read — a plain field copy, not an accumulator, since a
// transcript file's own line-by-line summation already reflects the whole
// session.
func (s *Session) SetTranscriptUsage(u transcript.SessionUsage) {
	s.TranscriptTokens = u.Tokens
	s.TranscriptCostUSD = u.CostUSD
	s.TranscriptContentBytes = u.ContentBytes
}

// IsZero reports whether no mechanism fired this session. ProcessedBytes is
// deliberately not part of this check — see its field doc comment.
func (s *Session) IsZero() bool {
	return s.DedupHits == 0 && s.BudgetTrims == 0 && s.RetiredCalls == 0
}

// Lifetime is the running tally across every session, plus a count of how
// many sessions have contributed to it.
type Lifetime struct {
	Sessions           int   `json:"sessions"`
	DedupHits          int   `json:"dedupHits"`
	DedupBytes         int64 `json:"dedupBytes"`
	BudgetTrims        int   `json:"budgetTrims"`
	BudgetLinesOmitted int   `json:"budgetLinesOmitted"`
	BudgetBytesOmitted int64 `json:"budgetBytesOmitted"`
	RetiredCalls       int   `json:"retiredCalls"`
	RetiredBytes       int64 `json:"retiredBytes"`
	ProcessedBytes     int64 `json:"processedBytes"`
	// TranscriptTokens/TranscriptCostUSD/TranscriptContentBytes — see
	// Session's fields of the same name. Folded in Add like every other
	// field, excluded from IsZero (Lifetime has no IsZero; see Session's).
	TranscriptTokens       int64   `json:"transcriptTokens"`
	TranscriptCostUSD      float64 `json:"transcriptCostUsd"`
	TranscriptContentBytes int64   `json:"transcriptContentBytes"`
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
	l.BudgetBytesOmitted += s.BudgetBytesOmitted
	l.RetiredCalls += s.RetiredCalls
	l.RetiredBytes += s.RetiredBytes
	l.ProcessedBytes += s.ProcessedBytes
	l.TranscriptTokens += s.TranscriptTokens
	l.TranscriptCostUSD += s.TranscriptCostUSD
	l.TranscriptContentBytes += s.TranscriptContentBytes
}

// Percent computes winglet_pct = suppressed / processed * 100, where
// suppressed is dedup + budget-trim + retire bytes, all measured on the
// same original-byte-length unit (see Session.ProcessedBytes). ok is false
// when processedBytes is zero (nothing observed yet) — callers must render
// that as "no data yet," not "0%," per the spec's honesty requirement: 0%
// reads as "this doesn't work," a different claim than "nothing to measure
// yet."
func Percent(dedupBytes, budgetBytesOmitted, retiredBytes, processedBytes int64) (pct float64, ok bool) {
	if processedBytes == 0 {
		return 0, false
	}
	suppressed := dedupBytes + budgetBytesOmitted + retiredBytes
	return float64(suppressed) / float64(processedBytes) * 100, true
}

// PartPercent reports what fraction of processedBytes a single mechanism's
// bytes represent — the same shape as Percent but for one card instead of
// the combined hero figure. Same zero-processed-bytes guard.
func PartPercent(bytes, processedBytes int64) (pct float64, ok bool) {
	if processedBytes == 0 {
		return 0, false
	}
	return float64(bytes) / float64(processedBytes) * 100, true
}

// Stretch converts a suppressed-percentage into the "same package, Nx more
// headroom" multiplier framing: stretch = 1 / (1 - p). Guards pct >= 100 to
// avoid a divide-by-zero/Inf — suppressed bytes can't exceed processed bytes
// in practice, but this keeps a corrupted or hand-edited stats file from
// producing a nonsensical multiplier.
func Stretch(pct float64) float64 {
	if pct >= 100 {
		return 0
	}
	return 100 / (100 - pct)
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
