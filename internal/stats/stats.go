// Package stats tracks how much the other three mechanisms (ledger, phase's
// retirement path, and output budgeting) actually did, so a hook can surface
// a session-end receipt instead of leaving every substitution invisible.
//
// Session is the only thing persisted: one file per session_id under
// projectDir, wiped on SessionStart/PostCompact same as ledger/phase/retire
// (a receipt describing "this session's" activity is meaningless once the
// session it describes is gone), but never deleted once a session ends —
// see ListSessions. Project and cross-project totals are never separately
// stored; SumProject computes them fresh by summing every on-disk session
// file every time it's called, so a project's total and the sum of its
// sessions can never disagree. This used to be two lifecycles — Session plus
// a separately persisted, incrementally-folded Lifetime file — but that
// second ledger could drift from the session files it was supposed to
// summarize (e.g. a crash between marking a session "folded" and actually
// folding it), so it's gone; SumProject is the only rollup path now.
package stats

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/umitkaanusta/agent-winglet/internal/statedir"
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
	// TranscriptTokens, TranscriptCostUSD, and TranscriptContentBytes carry
	// this session's real transcript-derived usage (see internal/transcript)
	// — the input-side token total, its priced cost at real per-token
	// rates, and the raw content-byte size those tokens represent.
	// TranscriptContentBytes doubles as the real denominator for Percent
	// (see its doc comment): real usage data existing isn't "a mechanism
	// firing," so these are excluded from IsZero.
	TranscriptTokens       int64   `json:"transcriptTokens"`
	TranscriptCostUSD      float64 `json:"transcriptCostUsd"`
	TranscriptContentBytes int64   `json:"transcriptContentBytes"`
	// TranscriptOffset is the transcript byte offset already folded into the
	// three fields above via AddTranscriptUsage — purely internal
	// bookkeeping for PostToolUse's incremental reads (see
	// transcript.ReadSessionUsageFrom), never surfaced to the desktop app.
	// Persisted here (not in a separate file) so it travels with the same
	// load/mutate/save cycle every other per-session field already uses.
	TranscriptOffset int64 `json:"transcriptOffset"`
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

// AddTranscriptUsage folds one incremental transcript.ReadSessionUsageFrom
// delta onto the running total and advances TranscriptOffset past it — the
// PostToolUse-time counterpart to SetTranscriptUsage's SessionEnd-time
// one-shot copy. Called on every PostToolUse (see cmd/ledger-hook's
// handlePostToolUse), so the desktop app's tokens/$ figures move live
// instead of staying at zero until the session ends.
func (s *Session) AddTranscriptUsage(delta transcript.SessionUsage, newOffset int64) {
	s.TranscriptTokens += delta.Tokens
	s.TranscriptCostUSD += delta.CostUSD
	s.TranscriptContentBytes += delta.ContentBytes
	s.TranscriptOffset = newOffset
}

// IsZero reports whether no mechanism fired this session. Transcript usage
// is deliberately not part of this check — see TranscriptTokens' doc
// comment.
func (s *Session) IsZero() bool {
	return s.DedupHits == 0 && s.BudgetTrims == 0 && s.RetiredCalls == 0
}

// Rollup is a plain summed total across a set of sessions, plus a count of
// how many sessions contributed to it — the shape SumProject returns.
// Unlike the old Lifetime type, a Rollup is never itself persisted: it's
// always recomputed from on-disk session files (see SumProject), so there's
// nothing for it to drift out of sync with.
type Rollup struct {
	Sessions           int
	DedupHits          int
	DedupBytes         int64
	BudgetTrims        int
	BudgetLinesOmitted int
	BudgetBytesOmitted int64
	RetiredCalls       int
	RetiredBytes       int64

	TranscriptTokens       int64
	TranscriptCostUSD      float64
	TranscriptContentBytes int64
}

// add folds one session's tally into the rollup and counts it toward
// Sessions.
func (r *Rollup) add(s *Session) {
	r.Sessions++
	r.DedupHits += s.DedupHits
	r.DedupBytes += s.DedupBytes
	r.BudgetTrims += s.BudgetTrims
	r.BudgetLinesOmitted += s.BudgetLinesOmitted
	r.BudgetBytesOmitted += s.BudgetBytesOmitted
	r.RetiredCalls += s.RetiredCalls
	r.RetiredBytes += s.RetiredBytes
	r.TranscriptTokens += s.TranscriptTokens
	r.TranscriptCostUSD += s.TranscriptCostUSD
	r.TranscriptContentBytes += s.TranscriptContentBytes
}

// Percent computes winglet_pct = suppressed / total * 100, where suppressed
// is dedup + budget-trim + retire bytes, and total is the real total bytes
// that counted toward usage: transcriptContentBytes (the transcript's actual
// content bytes — what really got sent to the model, covering every tool's
// output, not just the mechanisms this package suppressed) plus suppressed
// itself (what would have been sent too, had it not been suppressed). This
// used to be measured against a narrower "processedBytes" tally covering
// only Bash output and post-boundary retired calls, which made the
// percentage answer "how much of the bytes we bothered to evaluate did we
// suppress" rather than "how much of this session's real usage did we
// suppress" — misleadingly high, since most tool output (Read/Edit/Grep/
// etc.) never entered that narrower pool at all.
//
// ok is false when transcriptContentBytes is zero (no real usage data yet,
// e.g. before a session's transcript has been read at SessionEnd) —
// callers must render that as "no data yet," not "0%" or a spuriously high
// percentage computed against an incomplete total, per the spec's honesty
// requirement: 0% reads as "this doesn't work," a different claim than
// "nothing to measure yet."
func Percent(dedupBytes, budgetBytesOmitted, retiredBytes, transcriptContentBytes int64) (pct float64, ok bool) {
	if transcriptContentBytes == 0 {
		return 0, false
	}
	suppressed := dedupBytes + budgetBytesOmitted + retiredBytes
	total := transcriptContentBytes + suppressed
	return float64(suppressed) / float64(total) * 100, true
}

// PartPercent reports what fraction of total a single mechanism's bytes
// represent — the same shape as Percent but for one card/bar instead of the
// combined hero figure. Callers pass the same real total Percent computed
// (transcriptContentBytes + suppressed), not a raw byte count of their own.
func PartPercent(bytes, total int64) (pct float64, ok bool) {
	if total == 0 {
		return 0, false
	}
	return float64(bytes) / float64(total) * 100, true
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

func sessionPath(projectDir, sessionID string) (string, error) {
	d, err := statedir.Dir(projectDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, sessionID+".stats.json"), nil
}

// LifetimeFileName is the on-disk name of the pre-Rollup lifetime ledger.
// The type that used to live there is gone (see the package doc), but the
// name is still needed to (a) exclude a leftover file from ListSessions —
// its JSON shape overlaps enough of Session's fields that parsing it as a
// session would silently double-count an old install's history — and (b)
// let a caller migrate that leftover file's data forward (see
// cmd/ledger-hook's migrateLegacyData).
const LifetimeFileName = "lifetime.stats.json"

func LoadSession(projectDir, sessionID string) (*Session, error) {
	p, err := sessionPath(projectDir, sessionID)
	if err != nil {
		return nil, err
	}
	var s Session
	if err := loadJSON(p, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

func SaveSession(projectDir, sessionID string, s *Session) error {
	p, err := sessionPath(projectDir, sessionID)
	if err != nil {
		return err
	}
	return saveJSON(p, s)
}

// InvalidateSession resets the session's mechanism counters (dedup,
// budget-trim, retire). Called on SessionStart and PostCompact, same as
// ledger.Invalidate/phase.Invalidate/retire.Invalidate, so a resumed or
// compacted session doesn't report a receipt describing activity from a
// part of the session that's now gone.
//
// The transcript-usage fields (TranscriptTokens, TranscriptCostUSD,
// TranscriptContentBytes, TranscriptOffset) are deliberately left alone:
// unlike the mechanism counters, they're not tied to ledger/phase state that
// a compact invalidates — they're a running total of real usage that already
// happened and stays true regardless of a later compact. This used to
// os.Remove the whole file, which self-healed once SessionEnd's full
// transcript re-read landed later — but if the process crashed or was
// force-quit between the compact and SessionEnd, that delete was
// destructive: the pre-compact usage was wiped from disk with nothing yet
// written to replace it, so it was gone for good. Resetting the mechanism
// fields in place instead of deleting the file removes that window.
//
// If resetting the mechanism counters leaves the session entirely zero
// (including transcript usage — true for a session with no recorded
// transcript activity yet), the file is still removed rather than left
// behind as an empty husk, matching the old behavior for that case.
func InvalidateSession(projectDir, sessionID string) error {
	p, err := sessionPath(projectDir, sessionID)
	if err != nil {
		return err
	}
	var s Session
	if err := loadJSON(p, &s); err != nil {
		return err
	}
	s.DedupHits = 0
	s.DedupBytes = 0
	s.BudgetTrims = 0
	s.BudgetLinesOmitted = 0
	s.BudgetBytesOmitted = 0
	s.RetiredCalls = 0
	s.RetiredBytes = 0

	if s.TranscriptTokens == 0 && s.TranscriptCostUSD == 0 && s.TranscriptContentBytes == 0 {
		err := os.Remove(p)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return saveJSON(p, &s)
}

// SessionFile identifies one on-disk session-stats file and its
// modification time — the closest thing to a session timestamp available,
// since Session itself carries no clock reading.
type SessionFile struct {
	ID      string
	ModTime time.Time
}

// ListSessions returns every session-stats file still on disk for
// projectDir (excluding LifetimeFileName), newest first by modification
// time. A project with no state dir yet (hook never fired here) returns an
// empty slice, not an error — same fail-soft convention the rest of this
// package follows.
func ListSessions(projectDir string) ([]SessionFile, error) {
	d, err := statedir.Dir(projectDir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var files []SessionFile
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || name == LifetimeFileName || !strings.HasSuffix(name, ".stats.json") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		files = append(files, SessionFile{
			ID:      strings.TrimSuffix(name, ".stats.json"),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].ModTime.After(files[j].ModTime) })
	return files, nil
}

// SumProject sums every session-stats file on disk for projectDir into a
// single Rollup. This is the only project or cross-project total this
// package produces — callers wanting an overall figure across projects sum
// multiple SumProject results themselves (see cmd/agent-winglet-app's
// GetOverview) rather than reading a separately maintained global file.
func SumProject(projectDir string) (Rollup, error) {
	files, err := ListSessions(projectDir)
	if err != nil {
		return Rollup{}, err
	}
	var r Rollup
	for _, f := range files {
		s, err := LoadSession(projectDir, f.ID)
		if err != nil {
			return Rollup{}, err
		}
		r.add(s)
	}
	return r, nil
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
