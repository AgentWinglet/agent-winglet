// Package stats tracks how much the other three mechanisms (ledger, phase's
// retirement path, and output budgeting) actually did, so a hook can surface
// a session-end receipt instead of leaving every substitution invisible.
//
// Session is the only thing persisted: one file per session_id under
// projectDir, accumulated for as long as that session_id is active —
// including across a compact or resume, since dedup/budget/retire savings
// already happened and neither event undoes bytes that were genuinely kept
// out of the model's context — and never deleted once a session ends, see
// ListSessions. Unlike Session, ledger/phase/retire's own state IS wiped on
// SessionStart/PostCompact, but that's a different thing: it's operational
// state for detecting future repeats/boundaries against content that's now
// gone from context, not a receipt of what already happened. Project and
// cross-project totals are never separately
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

const (
	AgentClaudeCode = "claude-code"
	AgentCodex      = "codex"
)

// Session is the running tally of mechanism activity for one session.
type Session struct {
	Agent              string `json:"agent"`
	DedupHits          int    `json:"dedupHits"`
	DedupBytes         int64  `json:"dedupBytes"`
	BudgetTrims        int    `json:"budgetTrims"`
	BudgetLinesOmitted int    `json:"budgetLinesOmitted"`
	BudgetBytesOmitted int64  `json:"budgetBytesOmitted"`
	RetiredCalls       int    `json:"retiredCalls"`
	RetiredBytes       int64  `json:"retiredBytes"`
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

// RecordRetire records one investigate call whose output of the given byte
// length was archived instead of replayed — either because the
// investigate→implement boundary was already crossed, or because the
// session's pre-boundary investigate-call threshold was exceeded (see
// cmd/claude-hook's investigateCallThreshold).
func (s *Session) RecordRetire(bytes int) {
	s.RetiredCalls++
	s.RetiredBytes += int64(bytes)
}

// SetTranscriptUsage copies a transcript read (see
// transcript.ReadSessionUsageWithOffset) onto the session. Called once per
// SessionEnd, after the transcript file has already been fully read — a
// plain field copy, not an accumulator, since a transcript file's own
// line-by-line summation already reflects the whole session.
//
// offset must be the exact byte count that read consumed (what
// ReadSessionUsageWithOffset returns alongside u), and is stored onto
// TranscriptOffset right along with the usage totals it priced. Skipping
// this used to leave TranscriptOffset stuck at whatever the last
// AddTranscriptUsage call (PostToolUse/Stop, mid-session) left it — fine
// while the session kept running, but wrong the moment it later got
// resumed (same session_id, transcript file kept growing): the next
// AddTranscriptUsage would resume from that stale offset and re-add
// everything between it and SessionEnd's end-of-file, double-counting
// content this full read already priced once. Keeping the two in the same
// call makes that pairing impossible to break by accident.
func (s *Session) SetTranscriptUsage(u transcript.SessionUsage, offset int64) {
	s.TranscriptTokens = u.Tokens
	s.TranscriptCostUSD = u.CostUSD
	s.TranscriptContentBytes = u.ContentBytes
	s.TranscriptOffset = offset
}

// AddTranscriptUsage folds one incremental transcript.ReadSessionUsageFrom
// delta onto the running total and advances TranscriptOffset past it — the
// PostToolUse-time counterpart to SetTranscriptUsage's SessionEnd-time
// one-shot copy. Called on every PostToolUse (see cmd/claude-hook's
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

// TokensSaved estimates the tokens this session's suppressed bytes
// represent, scaled by this session's own tokens-per-content-byte density.
// It's deliberately computed per session and summed by Rollup.add (see
// Rollup.TokensSaved) rather than re-derived from an aggregate percentage at
// rollup time: tokens*pct/(100-pct) is a ratio, and a ratio computed from
// summed inputs isn't the same number as the sum of that same ratio computed
// per session when sessions have different suppression densities — which
// used to make a project's "tokens saved" figure disagree with the sum of
// its sessions' own figures. ok is false under the same "no data yet"
// condition as Percent: TranscriptContentBytes == 0.
func (s *Session) TokensSaved() (tokens float64, ok bool) {
	pct, ok := Percent(s.DedupBytes, s.BudgetBytesOmitted, s.RetiredBytes, s.TranscriptContentBytes)
	if !ok {
		return 0, false
	}
	return float64(s.TranscriptTokens) * pct / (100 - pct), true
}

// DollarSaved prices TokensSaved at this session's own real cost-per-token
// rate (TranscriptCostUSD / TranscriptTokens) — see TokensSaved's doc
// comment for why this must be computed per session and summed, not
// re-derived at rollup time. ok is false when TokensSaved has no data yet,
// or this session has no priced tokens to derive a rate from.
func (s *Session) DollarSaved() (dollars float64, ok bool) {
	tokensSaved, ok := s.TokensSaved()
	if !ok || s.TranscriptTokens == 0 {
		return 0, false
	}
	costPerToken := s.TranscriptCostUSD / float64(s.TranscriptTokens)
	return tokensSaved * costPerToken, true
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

	// TokensSaved and DollarSaved are literal sums of each session's own
	// Session.TokensSaved()/DollarSaved() — see those methods' doc comments
	// for why summing per-session values, rather than re-deriving from this
	// rollup's own aggregate percentage, is what keeps a project's total
	// equal to the sum of its sessions.
	TokensSaved float64
	DollarSaved float64
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

	if tokensSaved, ok := s.TokensSaved(); ok {
		r.TokensSaved += tokensSaved
	}
	if dollarSaved, ok := s.DollarSaved(); ok {
		r.DollarSaved += dollarSaved
	}
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
// percentage computed against an incomplete total: 0% reads as "this
// doesn't work," a different claim than "nothing to measure yet."
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
// cmd/claude-hook's migrateLegacyData).
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
	s.ensureAgent()
	return &s, nil
}

func SaveSession(projectDir, sessionID string, s *Session) error {
	p, err := sessionPath(projectDir, sessionID)
	if err != nil {
		return err
	}
	s.ensureAgent()
	return saveJSON(p, s)
}

func (s *Session) ensureAgent() {
	if s.Agent == "" {
		s.Agent = AgentClaudeCode
	}
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
