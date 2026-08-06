// ledger-hook is the Claude Code hook binary for agent-winglet. It is
// registered for two hook events:
//
//   - PostToolUse (all tools — see settings.json/install.sh, no matcher):
//     on Bash, an exact-repeat of a previously seen command's output
//     replaces the output with a compact "unchanged since turn N" reference
//     via updatedToolOutput; a first-time, successful command whose stdout
//     is long is replaced with a head/tail receipt (output budgeting by
//     outcome). Pre-boundary, Grep's content field and Glob's filenames
//     array get the same head/tail budgeting treatment as Bash stdout once
//     they cross the line-count threshold (see
//     handleGrepBudget/handleGlobBudget) — WebFetch, WebSearch, and Read are
//     deliberately excluded, see handlePostToolUse's default case for why.
//     On the first implement-classified call (Edit/Write/
//     NotebookEdit) after at least one investigate-classified call
//     (Read/Grep/Glob/WebFetch/WebSearch/Task) this session, emits a
//     one-time suggestion to compact (see handlePhaseBoundary). Once that
//     boundary has already been crossed, any further investigate-classified
//     call has its own output archived to disk and replaced with a compact
//     receipt (see handleRetireInvestigate) instead of budgeted — budgeting
//     only ever applies pre-boundary, where retirement doesn't fire yet.
//     Anything else passes through untouched.
//   - SessionStart / PostCompact: deletes the session's ledger, phase state,
//     retired-content directory, and stats tally so no substitution,
//     boundary suggestion, retired receipt, or session receipt survives a
//     restart or compaction (a hard constraint). Also registers the current
//     project — resolved via internal/projectroot.Resolve(in.Cwd), the git
//     root rather than the raw cwd, so sessions started from different
//     subdirectories of the same project collapse into one identity — in the
//     global ~/.agent-winglet/projects.json registry (see
//     internal/registry.Register), and best-effort migrates any pre-move-
//     storage per-cwd state it finds into that project's new global state
//     dir (see migrateLegacyData). The hook installs globally now, so
//     SessionStart/PostCompact is the only place a project gets registered.
//   - Stop: records the transcript-usage delta since the last recorded
//     offset, the same call PostToolUse makes (see recordTranscriptDelta),
//     so a session that never calls a tool — pure chat, no Bash/Read/Edit/
//     etc. — still gets a stats file written after its first turn instead of
//     only at SessionEnd. Without this, such a session had no stats file on
//     disk at all until SessionEnd fired; if the process crashed or was
//     force-quit before then, its usage was silently excluded from every
//     project/overall total, even though it consumed real tokens. Stop fires
//     after every turn (not just the last one), so this closes that gap for
//     all but the final, still-in-flight turn.
//   - SessionEnd: emits a one-time "savings receipt" systemMessage
//     summarizing what the above mechanisms did this session, plus the
//     project's lifetime total — computed fresh across every session file on
//     disk, not a separately maintained running total (see
//     internal/stats.SumProject and handleSessionEnd). SessionEnd was
//     confirmed to support the systemMessage output
//     field (a universal field, per the hooks reference) on 2026-08-03 —
//     unlike hookSpecificOutput.additionalContext, which is not documented
//     for this event and isn't needed here since no further model turn
//     follows session end. Emits nothing if no mechanism fired this session
//     (see handleSessionEnd) or if AGENT_WINGLET_QUIET is set.
//
// Read is intentionally not handled by the Ledger (repeat-detection) path:
// Claude Code already detects an unchanged file natively and returns
// tool_response.type == "file_unchanged" on a repeat Read, confirmed by
// inspecting real PostToolUse payloads. Adding ledger logic for Read would
// duplicate a capability the harness already provides for free. Read is
// still used, separately, as an investigate-classified signal for the phase
// boundary and for post-boundary retirement.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/umitkaanusta/agent-winglet/internal/config"
	"github.com/umitkaanusta/agent-winglet/internal/ledger"
	"github.com/umitkaanusta/agent-winglet/internal/phase"
	"github.com/umitkaanusta/agent-winglet/internal/projectroot"
	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/retire"
	"github.com/umitkaanusta/agent-winglet/internal/statedir"
	"github.com/umitkaanusta/agent-winglet/internal/stats"
	"github.com/umitkaanusta/agent-winglet/internal/transcript"
)

// legacySessionID is the synthetic session file that absorbs pre-Rollup
// history during migration (see migrateLegacyData). Never a real Claude Code
// session_id (those are UUIDs), so it can't collide with one; it just
// participates in stats.SumProject like any other session file from then on.
const legacySessionID = "legacy-migrated"

// Output budgeting by outcome: a first-time (non-repeat) command that
// succeeded but produced a long stdout gets collapsed to its head and tail.
// The Bash tool_response schema (confirmed via the docs' PostToolUse section)
// exposes stdout/stderr/interrupted/isImage but no exit code, so "succeeded"
// uses the same proxy the repeat-check already relies on: empty stderr, not
// interrupted, not an image.
const (
	budgetLineThreshold = 60
	budgetHeadLines     = 15
	budgetTailLines     = 15
)

// budgetBody collapses body to its first budgetHeadLines and last
// budgetTailLines lines if it has more than budgetLineThreshold lines. It
// reports ok == false when body is short enough to leave untouched, and
// omittedLines/omittedBytes as the size of the dropped middle section (both
// 0 when ok is false). omittedBytes is the "\n"-joined byte length of just
// the dropped lines — the same original-size unit RecordDedup/RecordRetire
// use — so budget-trims can contribute to the same suppressed-bytes total.
//
// Unlike handleRetireInvestigate's archive-then-receipt pattern, budgeting
// used to just discard the dropped middle outright — recoverable only by
// re-issuing the same tool call. That's a real gap: both papers this
// mechanism is modeled on (see AGENTDIET_COMPARISON.md) only ever act on
// content that's already aged out — AgentDiet's reflection module has a
// mandatory 2-step delay before it will touch a step, and Complexity Trap's
// Observation Masking only masks observations older than its rolling
// window — never the freshest turn. AgentDiet's own Step/PStep metrics
// (Table 2) confirm the failure mode directly: cutting too aggressively
// measurably increases the number of steps an agent needs, because it has
// to "recover the disrupted information through additional tool calls."
// Budgeting fires on arrival, before the agent has even read the output
// once, which is exactly the risky case those papers avoid — so every trim
// now archives the full pre-trim body via retire.Store (root/sessionID) and
// names that path in the notice line, the same way a retired investigate
// call does. If the agent needed what got cut, recovery is one cheap Read
// of a local path instead of re-running the tool.
//
// notice composes the omission marker's text (everything after "N <unit>
// omitted" in the receipt line) — callers vary it because Bash's marker also
// carries an outcome ("exit 0") that other tools' bodies have no equivalent
// of; archivePath is threaded through so every notice can point at the same
// recovery path. This is the tool-agnostic core behind budgetStdout (Bash)
// and the Grep/Glob budget handlers below (see handlePostToolUse's package
// doc for which tool_response field each one budgets and why
// WebFetch/WebSearch/Read are deliberately not among them).
func budgetBody(body, root, sessionID string, notice func(omitted int, archivePath string) string) (budgeted string, omittedLines int, omittedBytes int64, ok bool, err error) {
	lines := strings.Split(body, "\n")
	trailingNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	n := len(lines)
	if n <= budgetLineThreshold {
		return "", 0, 0, false, nil
	}

	archivePath, err := retire.Store(root, sessionID, []byte(body))
	if err != nil {
		return "", 0, 0, false, err
	}

	dropped := lines[budgetHeadLines : n-budgetTailLines]
	omittedLines = len(dropped)
	omittedBytes = int64(len(strings.Join(dropped, "\n")))

	var b strings.Builder
	b.WriteString(strings.Join(lines[:budgetHeadLines], "\n"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "[agent-winglet] %s\n", notice(omittedLines, archivePath))
	b.WriteString(strings.Join(lines[n-budgetTailLines:], "\n"))
	if trailingNewline {
		b.WriteString("\n")
	}
	return b.String(), omittedLines, omittedBytes, true, nil
}

// budgetStdout is budgetBody specialized for Bash stdout: same head/tail
// collapse, plus the "exit 0" outcome tag in the marker (this call site
// already knows the command succeeded — see its caller in
// handleBashPostToolUse).
func budgetStdout(stdout, root, sessionID string) (budgeted string, omittedLines int, omittedBytes int64, ok bool, err error) {
	return budgetBody(stdout, root, sessionID, func(omitted int, archivePath string) string {
		return fmt.Sprintf("%d lines omitted, exit 0 (showing first %d/last %d) — full output at %s",
			omitted, budgetHeadLines, budgetTailLines, archivePath)
	})
}

// budgetTextField is budgetBody specialized for a tool_response field that's
// just freeform text with no Bash-style outcome to report (Grep's content
// field) — same marker as budgetStdout minus the ", exit 0" clause.
func budgetTextField(body, root, sessionID string) (budgeted string, omittedLines int, omittedBytes int64, ok bool, err error) {
	return budgetBody(body, root, sessionID, func(omitted int, archivePath string) string {
		return fmt.Sprintf("%d lines omitted (showing first %d/last %d) — full output at %s",
			omitted, budgetHeadLines, budgetTailLines, archivePath)
	})
}

// budgetEntryList is budgetBody specialized for a tool_response field that's
// an array of short entries rather than freeform text (Glob's filenames) —
// the array is newline-joined into a body budgetBody can operate on line by
// line (one path per line), and the marker calls the unit "entries" instead
// of "lines" since that's what a reader actually sees collapsed.
func budgetEntryList(entries []string, root, sessionID string) (budgeted string, omittedEntries int, omittedBytes int64, ok bool, err error) {
	return budgetBody(strings.Join(entries, "\n"), root, sessionID, func(omitted int, archivePath string) string {
		return fmt.Sprintf("%d entries omitted (showing first %d/last %d) — full list at %s",
			omitted, budgetHeadLines, budgetTailLines, archivePath)
	})
}

type hookInput struct {
	SessionID      string          `json:"session_id"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
	TranscriptPath string          `json:"transcript_path"`
}

type bashOutput struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Interrupted bool   `json:"interrupted"`
	IsImage     bool   `json:"isImage"`
}

// grepResponse and globResponse mirror GrepOutput and GlobOutput from the
// Claude Code CLI's own sdk-tools.d.ts (the installed @anthropic-ai/claude-
// code npm package ships this file; there is no public docs page for
// tool_response schemas — same gap the bashOutput comment above already
// notes for Bash). Confirmed against the shipped .d.ts rather than a live
// capture — no Grep/Glob tools were available to trigger live in the
// session this was developed in — but still a confirmed schema, not a
// guess, just from a different source than a live capture would be.
//
// Each struct round-trips every field the real output can carry (not just
// the one being budgeted), so replacing UpdatedToolOutput with one of these
// after budgeting never drops information the original response had, unlike
// bashOutput's deliberately-minimal 4-field subset above.
type grepResponse struct {
	Mode          string   `json:"mode,omitempty"`
	NumFiles      int      `json:"numFiles"`
	Filenames     []string `json:"filenames"`
	Content       string   `json:"content,omitempty"`
	NumLines      int      `json:"numLines,omitempty"`
	NumMatches    int      `json:"numMatches,omitempty"`
	TotalFiles    int      `json:"totalFiles,omitempty"`
	TotalLines    int      `json:"totalLines,omitempty"`
	AppliedLimit  int      `json:"appliedLimit,omitempty"`
	AppliedOffset int      `json:"appliedOffset,omitempty"`
}

type globResponse struct {
	DurationMs      int64    `json:"durationMs"`
	NumFiles        int      `json:"numFiles"`
	Filenames       []string `json:"filenames"`
	Truncated       bool     `json:"truncated"`
	TotalMatches    int      `json:"totalMatches,omitempty"`
	CountIsComplete bool     `json:"countIsComplete,omitempty"`
}

type hookSpecificOutput struct {
	HookEventName     string      `json:"hookEventName"`
	UpdatedToolOutput interface{} `json:"updatedToolOutput,omitempty"`
	AdditionalContext string      `json:"additionalContext,omitempty"`
}

type hookOutput struct {
	SystemMessage      string             `json:"systemMessage,omitempty"`
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ledger-hook:", err)
		os.Exit(1)
	}
}

func run() error {
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return err
	}

	var in hookInput
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}

	out, err := handle(in)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	return json.NewEncoder(os.Stdout).Encode(out)
}

// handle contains all decision logic, with no I/O beyond the ledger's state
// file. It returns the hookOutput to encode to stdout, or nil if the hook
// has nothing to report (equivalent to exit 0 with no output).
func handle(in hookInput) (*hookOutput, error) {
	switch in.HookEventName {
	case "SessionStart", "PostCompact":
		root := projectroot.Resolve(in.Cwd)
		migrateLegacyData(in.Cwd, root)

		if err := ledger.Invalidate(root, in.SessionID); err != nil {
			return nil, err
		}
		if err := phase.Invalidate(root, in.SessionID); err != nil {
			return nil, err
		}
		if err := retire.Invalidate(root, in.SessionID); err != nil {
			return nil, err
		}
		if err := stats.InvalidateSession(root, in.SessionID); err != nil {
			return nil, err
		}
		// Self-registration: now that the hook installs globally rather than
		// per-project (see install.sh), there's no install-time moment inside
		// each project to register it in ~/.agent-winglet/projects.json.
		// Registering here means the first session the hook ever sees for a
		// given project root adds that project to the registry.
		return nil, registry.Register(root)
	case "PostToolUse":
		return handlePostToolUse(in)
	case "Stop":
		return nil, recordTranscriptDelta(projectroot.Resolve(in.Cwd), in)
	case "SessionEnd":
		return handleSessionEnd(in)
	}
	return nil, nil
}

// legacyLifetime mirrors the JSON shape of a pre-Rollup lifetime.stats.json
// file (the type that used to live there, stats.Lifetime, no longer exists —
// see internal/stats' package doc) so migrateLegacyData can still read one
// left over from before this codebase switched project/overall totals to a
// pure sum of session files.
type legacyLifetime struct {
	DedupHits              int     `json:"dedupHits"`
	DedupBytes             int64   `json:"dedupBytes"`
	BudgetTrims            int     `json:"budgetTrims"`
	BudgetLinesOmitted     int     `json:"budgetLinesOmitted"`
	BudgetBytesOmitted     int64   `json:"budgetBytesOmitted"`
	RetiredCalls           int     `json:"retiredCalls"`
	RetiredBytes           int64   `json:"retiredBytes"`
	TranscriptTokens       int64   `json:"transcriptTokens"`
	TranscriptCostUSD      float64 `json:"transcriptCostUsd"`
	TranscriptContentBytes int64   `json:"transcriptContentBytes"`
}

// migrateLegacyData reclaims two things that predate stats.SumProject's
// sum-of-session-files model, both by folding their counts onto the
// legacy-migrated seed session (see legacySessionID) rather than into a
// second persisted ledger of their own:
//
//  1. A pre-move-storage install's per-project state
//     (<cwd>/.claude/agent-winglet/lifetime.stats.json). Only the lifetime
//     figures have lasting value — everything else in that directory is
//     session-scoped and was already designed to never survive a restart,
//     so losing it mid-upgrade is a non-event.
//  2. This project's own current-layout lifetime.stats.json (root's state
//     dir), from before this switch — otherwise its history would just be
//     silently orphaned on disk, never summed again.
//
// This runs on every SessionStart/PostCompact so a stale per-subdirectory
// dir left over from the old cwd-keyed layout gets folded in and cleaned up
// independently, the next time a session happens to start from that
// particular subdirectory. Each fold reads-mutates-saves the same seed
// session (see mergeLegacyLifetime), so calling it repeatedly, or across
// multiple old dirs, never double-counts.
//
// Best-effort: errors are logged to stderr, never surfaced to the hook's
// caller, matching this codebase's existing fail-soft convention for state
// I/O.
func migrateLegacyData(cwd, root string) {
	legacyDir := filepath.Join(cwd, ".claude", "agent-winglet")
	if info, err := os.Stat(legacyDir); err == nil && info.IsDir() {
		mergeLegacyLifetime(root, filepath.Join(legacyDir, stats.LifetimeFileName))
		if err := os.RemoveAll(legacyDir); err != nil {
			fmt.Fprintln(os.Stderr, "ledger-hook: migration: removing legacy dir failed:", err)
		}
	}

	if d, err := statedir.Dir(root); err == nil {
		mergeLegacyLifetime(root, filepath.Join(d, stats.LifetimeFileName))
	}
}

// mergeLegacyLifetime folds one old lifetime.stats.json's counts onto the
// legacy-migrated seed session and removes the file. A missing or unreadable
// path is a silent no-op (nothing to migrate); a corrupt file is logged and
// skipped, leaving the file in place rather than losing its data.
func mergeLegacyLifetime(root, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var old legacyLifetime
	if err := json.Unmarshal(data, &old); err != nil {
		fmt.Fprintln(os.Stderr, "ledger-hook: migration: corrupt legacy lifetime stats, skipping merge:", err)
		return
	}

	seed, err := stats.LoadSession(root, legacySessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ledger-hook: migration: loading seed session failed:", err)
		return
	}
	seed.DedupHits += old.DedupHits
	seed.DedupBytes += old.DedupBytes
	seed.BudgetTrims += old.BudgetTrims
	seed.BudgetLinesOmitted += old.BudgetLinesOmitted
	seed.BudgetBytesOmitted += old.BudgetBytesOmitted
	seed.RetiredCalls += old.RetiredCalls
	seed.RetiredBytes += old.RetiredBytes
	seed.TranscriptTokens += old.TranscriptTokens
	seed.TranscriptCostUSD += old.TranscriptCostUSD
	seed.TranscriptContentBytes += old.TranscriptContentBytes
	if err := stats.SaveSession(root, legacySessionID, seed); err != nil {
		fmt.Fprintln(os.Stderr, "ledger-hook: migration: saving seed session failed:", err)
		return
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintln(os.Stderr, "ledger-hook: migration: removing legacy lifetime file failed:", err)
	}
}

// investigateTools and implementTools classify a PostToolUse tool_name for
// the investigate→implement phase boundary (handlePhaseBoundary). Bash is
// deliberately left unclassified: tool_name alone can't tell a read-only
// command (git status, cat) from a mutating one (git commit, sed -i), and a
// wrong guess in either direction is worse than treating it as neutral.
// Classifying Bash by parsing the command itself is a natural follow-up once
// this first-pass, tool_name-only signal proves useful.
var investigateTools = map[string]bool{
	"Read":      true,
	"Grep":      true,
	"Glob":      true,
	"WebFetch":  true,
	"WebSearch": true,
	"Task":      true, // subagent dispatch — commonly used for exploration
}

var implementTools = map[string]bool{
	"Edit":         true,
	"Write":        true,
	"NotebookEdit": true,
}

// handlePhaseBoundary suggests running /compact once the session has moved
// from investigating to implementing. Claude Code has no hook mechanism to
// trigger compaction programmatically (confirmed against the hooks
// reference: PreCompact can only observe or block a compaction already under
// way), so on the first implement-classified call after at least one
// investigate-classified call this session, it can only suggest — via both
// systemMessage (shown directly to the user) and additionalContext (fed to
// the model, in case the user doesn't notice systemMessage or the agent
// should act on it, e.g. by proposing /compact itself). Fires at most once
// per session (phase.State.Observe's latch), so it never nags on every
// subsequent edit.
//
// It also reports pastBoundary: whether the session has already crossed the
// boundary as of this call (crossed just now, or earlier). handlePostToolUse
// uses this to decide whether to retire a later investigate call's output
// (see handleRetireInvestigate). That's the only direction retirement can
// go: a PostToolUse hook can only ever rewrite the tool call it's currently
// processing, never an earlier one, so already-replayed investigate output
// can't be rewritten after the fact — only investigate calls made after the
// boundary crossing can be.
func handlePhaseBoundary(in hookInput, root string) (out *hookOutput, pastBoundary bool, err error) {
	isInvestigate := investigateTools[in.ToolName]
	isImplement := implementTools[in.ToolName]
	if !isInvestigate && !isImplement {
		return nil, false, nil
	}

	st, err := phase.Load(root, in.SessionID)
	if err != nil {
		return nil, false, err
	}
	crossed := st.Observe(isInvestigate, isImplement)
	if err := phase.Save(root, in.SessionID, st); err != nil {
		return nil, false, err
	}
	pastBoundary = st.Suggested
	if !crossed {
		return nil, pastBoundary, nil
	}

	const msg = "[agent-winglet] investigation looks done and implementation is " +
		"starting — this is a natural point to run /compact, while what's " +
		"still relevant is still clear, rather than waiting for the context " +
		"window to fill up."
	return &hookOutput{
		SystemMessage: msg,
		HookSpecificOutput: hookSpecificOutput{
			HookEventName:     "PostToolUse",
			AdditionalContext: msg,
		},
	}, pastBoundary, nil
}

// investigateKeyFields covers the tool_input field names, across
// investigateTools, that identify what a call was for. Unrecognized/missing
// fields just leave the corresponding string empty — investigateKey falls
// back to the raw tool_input in that case.
type investigateKeyFields struct {
	FilePath    string `json:"file_path"`
	Pattern     string `json:"pattern"`
	URL         string `json:"url"`
	Query       string `json:"query"`
	Description string `json:"description"`
}

// investigateKey extracts a short human-readable identifier (file path,
// grep/glob pattern, URL, search query, or subagent task description) from
// an investigate-classified tool call's input, for use in a retire receipt.
func investigateKey(toolInput json.RawMessage) string {
	var f investigateKeyFields
	_ = json.Unmarshal(toolInput, &f)
	switch {
	case f.FilePath != "":
		return f.FilePath
	case f.Pattern != "":
		return f.Pattern
	case f.URL != "":
		return f.URL
	case f.Query != "":
		return f.Query
	case f.Description != "":
		return f.Description
	default:
		return string(toolInput)
	}
}

// handleRetireInvestigate retires used-up investigate output: once the
// session has already crossed the investigate→implement boundary (see
// handlePhaseBoundary), any further investigate-classified call's own
// output — the one thing a PostToolUse hook can still rewrite — is
// archived to disk and replaced with a compact receipt, instead of
// replaying it in full. Called only when in.ToolName is investigate-
// classified and the boundary has already been crossed.
func handleRetireInvestigate(in hookInput, root string) (*hookOutput, error) {
	path, err := retire.Store(root, in.SessionID, in.ToolResponse)
	if err != nil {
		return nil, err
	}
	key := investigateKey(in.ToolInput)
	n := len(in.ToolResponse)
	receipt := fmt.Sprintf(
		"[agent-winglet] investigate output retired post-boundary (%s %s, %d bytes) — full content at %s",
		in.ToolName, key, n, path,
	)
	if err := recordStat(root, in.SessionID, func(s *stats.Session) {
		s.RecordRetire(n)
	}); err != nil {
		return nil, err
	}
	return &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     "PostToolUse",
		UpdatedToolOutput: receipt,
	}}, nil
}

// recordStat loads the session's stats tally, applies mutate, and saves it
// back — the same load/mutate/save shape as the ledger and phase call sites
// already use, just with the stats package's own state instead.
func recordStat(projectDir, sessionID string, mutate func(*stats.Session)) error {
	s, err := stats.LoadSession(projectDir, sessionID)
	if err != nil {
		return err
	}
	mutate(s)
	return stats.SaveSession(projectDir, sessionID, s)
}

// recordTranscriptDelta reads whatever the transcript gained since this
// session's last recorded offset and folds it in — the incremental
// counterpart to handleSessionEnd's one-shot full read. Bounded to new
// content only (transcript.ReadSessionUsageFrom), so running this on every
// PostToolUse/Stop costs O(this call's new lines), not O(the whole
// transcript so far) — the latter would make hook latency grow with session
// length, in a tool whose entire purpose is cutting round-trip cost, not
// adding to it. No-ops (cheaply) when nothing new has landed since the last
// call. Called from both PostToolUse (every tool call) and Stop (every
// turn, including tool-free ones) so a session's stats file exists and
// stays current even if it never calls a tool — see the package doc's Stop
// entry.
func recordTranscriptDelta(projectDir string, in hookInput) error {
	s, err := stats.LoadSession(projectDir, in.SessionID)
	if err != nil {
		return err
	}
	delta, newOffset, err := transcript.ReadSessionUsageFrom(in.TranscriptPath, s.TranscriptOffset)
	if err != nil {
		return err
	}
	if newOffset == s.TranscriptOffset {
		return nil
	}
	s.AddTranscriptUsage(delta, newOffset)
	return stats.SaveSession(projectDir, in.SessionID, s)
}

func handlePostToolUse(in hookInput) (*hookOutput, error) {
	root := projectroot.Resolve(in.Cwd)

	// Live transcript-usage tracking runs unconditionally, before any of the
	// tool-specific branches below (several of which return early) — the
	// transcript grows on every tool call, not just the ones a mechanism
	// below fires on, so this can't be folded into any single branch's
	// early return without under-counting the calls that take other paths.
	if err := recordTranscriptDelta(root, in); err != nil {
		return nil, err
	}

	out, pastBoundary, err := handlePhaseBoundary(in, root)
	if err != nil {
		return nil, err
	}
	if out != nil {
		return out, nil
	}
	if investigateTools[in.ToolName] && pastBoundary {
		return handleRetireInvestigate(in, root)
	}

	// Everything past this point is pre-boundary-only: any investigate-
	// classified call that got this far without being retired above just
	// means the boundary hasn't crossed yet (or this tool isn't investigate-
	// classified at all, e.g. Bash). Budgeting and retirement are mutually
	// exclusive by construction — never double-applied to the same call —
	// because retirement already claimed the investigate+pastBoundary case.
	switch in.ToolName {
	case "Bash":
		return handleBashPostToolUse(in, root)
	case "Grep":
		return handleGrepBudget(in, root)
	case "Glob":
		return handleGlobBudget(in, root)
	default:
		// Notably absent: WebFetch, WebSearch, and Read.
		//
		// WebFetch's result field (confirmed live, via a real WebFetch call
		// in the session this was developed in — the WebFetchOutput shape in
		// the CLI's sdk-tools.d.ts matched exactly) isn't raw page content:
		// per the tool's own description, it's already been fetched,
		// converted to markdown, and answered by a small model against the
		// caller's specific prompt — plus the tool self-summarizes when the
		// source page is huge. That means it's rarely long enough to need
		// budgeting in the first place, and on the rare occasions it is,
		// it's because the prompt asked for something extensive — head/tail
		// truncation would be cutting the requested content itself, not
		// waste, unlike Bash's setup/noise/result shape or Grep/Glob's
		// independent, interchangeable entries.
		//
		// WebSearch's tool_response (also confirmed live) is a heterogeneous
		// results array mixing a {tool_use_id, content: [{title,url}]} block
		// with a plain-string commentary element — no single freeform field
		// budgetBody can act on the way it can for Grep's content, and
		// Claude Code's own search result counts are already small in
		// practice.
		//
		// Read has no confirmed tool_response schema at all — it's absent
		// from the CLI's shipped sdk-tools.d.ts output types entirely
		// (unlike Grep/Glob, which are both there) — and guessing a
		// file-content field to truncate risks silently corrupting real
		// file content, a worse failure mode than leaving it untouched.
		//
		// All three are deliberate scope cuts, not oversights.
		return nil, nil
	}
}

// handleBashPostToolUse implements the ledger's Bash-specific path: repeat
// detection (dedup) first, output budgeting only for first-time calls that
// don't hit the ledger. Bash is not investigate-classified (see
// investigateTools' doc comment on why it's deliberately left
// unclassified), so it never goes through handleRetireInvestigate — dedup
// and budgeting are its only two suppression mechanisms.
func handleBashPostToolUse(in hookInput, root string) (*hookOutput, error) {
	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(in.ToolInput, &input); err != nil {
		return nil, nil
	}

	var response bashOutput
	if err := json.Unmarshal(in.ToolResponse, &response); err != nil {
		return nil, nil
	}
	if response.Interrupted || response.IsImage || response.Stderr != "" {
		// Don't substitute failures/interrupts/images: the non-lossy
		// guarantee only covers exact repeats of content the agent already
		// used successfully, and stderr may carry information the agent
		// needs on every occurrence, not just the first.
		return nil, nil
	}
	if response.Stdout == "" {
		return nil, nil
	}

	key := "Bash:" + input.Command

	st, err := ledger.Load(root, in.SessionID)
	if err != nil {
		return nil, err
	}

	processedBytes := len(response.Stdout)

	repeatOfTurn, isRepeat := st.Check(key, response.Stdout)
	if !isRepeat {
		if err := ledger.Save(root, in.SessionID, st); err != nil {
			return nil, err
		}
		budgeted, omittedLines, omittedBytes, ok, err := budgetStdout(response.Stdout, root, in.SessionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			// Short enough to leave untouched — nothing suppressed, nothing
			// to record.
			return nil, nil
		}
		if err := recordStat(root, in.SessionID, func(s *stats.Session) {
			s.RecordBudgetTrim(omittedLines, omittedBytes)
		}); err != nil {
			return nil, err
		}
		return &hookOutput{HookSpecificOutput: hookSpecificOutput{
			HookEventName:     "PostToolUse",
			UpdatedToolOutput: bashOutput{Stdout: budgeted},
		}}, nil
	}

	receipt := fmt.Sprintf("[agent-winglet] unchanged since turn %d (%s)", repeatOfTurn, key)

	repeatOut := &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     "PostToolUse",
		UpdatedToolOutput: bashOutput{Stdout: receipt},
	}}
	if err := ledger.Save(root, in.SessionID, st); err != nil {
		return nil, err
	}
	if err := recordStat(root, in.SessionID, func(s *stats.Session) {
		s.RecordDedup(processedBytes)
	}); err != nil {
		return nil, err
	}
	return repeatOut, nil
}

// handleGrepBudget budgets Grep's content-mode output (see grepResponse's
// doc comment for the confirmed schema). files_with_matches/count mode
// responses have no Content to speak of — NumFiles/Filenames/NumMatches are
// already compact — so an empty Content is treated the same as "too short
// to budget," matching handleBashPostToolUse's own empty-stdout check.
func handleGrepBudget(in hookInput, root string) (*hookOutput, error) {
	var resp grepResponse
	if err := json.Unmarshal(in.ToolResponse, &resp); err != nil {
		return nil, nil
	}
	if resp.Content == "" {
		return nil, nil
	}
	budgeted, omittedLines, omittedBytes, ok, err := budgetTextField(resp.Content, root, in.SessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if err := recordStat(root, in.SessionID, func(s *stats.Session) {
		s.RecordBudgetTrim(omittedLines, omittedBytes)
	}); err != nil {
		return nil, err
	}
	resp.Content = budgeted
	return &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     "PostToolUse",
		UpdatedToolOutput: resp,
	}}, nil
}

// handleGlobBudget budgets Glob's filenames array (see globResponse's doc
// comment for the confirmed schema) by collapsing it to its first/last
// entries, the same head/tail shape budgetStdout applies to Bash — just
// over path entries instead of output lines.
func handleGlobBudget(in hookInput, root string) (*hookOutput, error) {
	var resp globResponse
	if err := json.Unmarshal(in.ToolResponse, &resp); err != nil {
		return nil, nil
	}
	if len(resp.Filenames) == 0 {
		return nil, nil
	}
	budgeted, omittedEntries, omittedBytes, ok, err := budgetEntryList(resp.Filenames, root, in.SessionID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	if err := recordStat(root, in.SessionID, func(s *stats.Session) {
		s.RecordBudgetTrim(omittedEntries, omittedBytes)
	}); err != nil {
		return nil, err
	}
	resp.Filenames = strings.Split(budgeted, "\n")
	return &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     "PostToolUse",
		UpdatedToolOutput: resp,
	}}, nil
}

// quietEnvVar is the one piece of configurability in scope for the savings
// receipt: it suppresses the SessionEnd systemMessage while leaving every
// underlying mechanism (dedup, budgeting, retirement, the compact nudge)
// fully active, and while lifetime bookkeeping still happens.
const quietEnvVar = "AGENT_WINGLET_QUIET"

// quiet checks AGENT_WINGLET_QUIET first, falling back to the global
// ~/.agent-winglet/config.json Quiet field (which itself defaults to true,
// see config.Load) only when the env var is unset entirely. The env var wins
// whenever it's set (including explicitly to "0" or "false"), so
// AGENT_WINGLET_QUIET=0 remains the way to opt back into the receipt for a
// terminal session even though it's on by default.
func quiet() bool {
	if v := os.Getenv(quietEnvVar); v != "" {
		return v != "0" && v != "false"
	}
	cfg, err := config.Load()
	if err != nil {
		return true
	}
	return cfg.Quiet
}

// handleSessionEnd emits the session's savings receipt: a summary of what
// the ledger (dedup), output-budgeting, and retirement mechanisms did this
// session, plus the project's lifetime total (stats.SumProject, computed
// fresh across every session file — this one included, since it's saved
// below before the sum runs). If no mechanism fired this session, it emits
// nothing — a receipt reporting "0 things happened" is noise, not signal
// (see the package doc's zero-activity note). The session's final numbers
// are still persisted to disk either way, even when AGENT_WINGLET_QUIET
// suppresses the message itself, so they count toward every future rollup.
func handleSessionEnd(in hookInput) (*hookOutput, error) {
	root := projectroot.Resolve(in.Cwd)

	sess, err := stats.LoadSession(root, in.SessionID)
	if err != nil {
		return nil, err
	}
	hadMechanismActivity := !sess.IsZero()

	// Read errors are swallowed (fields stay zero) — the transcript is real
	// usage data if the read succeeds, and a "no data yet" fallback exists
	// downstream (see stats.Session.TranscriptTokens' doc comment) for
	// whenever it doesn't. This full re-read (as opposed to the incremental,
	// offset-based one PostToolUse uses — see recordTranscriptDelta) is the
	// authoritative reconciliation pass: it overwrites whatever got
	// accumulated turn-by-turn with one clean read of the whole final file.
	// The offset it also returns must be persisted right along with the
	// usage totals (see SetTranscriptUsage) so a later resume of this same
	// session_id picks its incremental reads back up from here, not from
	// whatever stale offset the last PostToolUse/Stop call left behind.
	if usage, offset, err := transcript.ReadSessionUsageWithOffset(in.TranscriptPath); err == nil {
		sess.SetTranscriptUsage(usage, offset)
	}

	// A session worth folding into lifetime is one with either suppression
	// activity or real transcript usage — not just the former. Before
	// PostToolUse tracked transcript usage live, "no mechanism fired" and
	// "nothing worth recording" were the same condition; they no longer are
	// — a session that only read/edited files (no Bash dedup/trim, no
	// post-boundary retire) still has a real, nonzero transcriptContentBytes
	// figure the desktop app's live Overview/Projects rollup needs.
	if !hadMechanismActivity && sess.TranscriptContentBytes == 0 {
		return nil, nil
	}

	// Persist the final transcript numbers onto the per-session file —
	// GetSessionStats (cmd/agent-winglet-app) reads this file back later for
	// the per-session breakdown, and without this write it would always see
	// zero transcript data for a completed session.
	if err := stats.SaveSession(root, in.SessionID, sess); err != nil {
		return nil, err
	}

	// The printed receipt stays gated on suppression activity specifically
	// — "0 things suppressed" is noise even when real transcript pricing
	// data exists to report (see receiptMessage's doc comment: it's a
	// suppression report, not a general usage report).
	if quiet() || !hadMechanismActivity {
		return nil, nil
	}

	rollup, err := stats.SumProject(root)
	if err != nil {
		return nil, err
	}
	return &hookOutput{
		SystemMessage: receiptMessage(sess, rollup),
		HookSpecificOutput: hookSpecificOutput{
			HookEventName: "SessionEnd",
		},
	}, nil
}

// receiptMessage composes the savings-receipt text. It reports only raw
// suppressed-content counts (dedup bytes, trimmed lines, retired bytes),
// framed explicitly as unvalidated — never a cost, token, or usage-cap
// savings figure, since no such measurement of *total session cost* exists
// (the paired-run harness came back inconclusive on the tasks tested so
// far). That framing is load-bearing, not throat-clearing: a self-reported
// "this saved you money" claim with no evidence behind it is the exact
// failure mode this receipt exists to avoid.
//
// The desktop app's Overview screen does now surface a priced dollar
// estimate (see stats.Session.TranscriptCostUSD, internal/pricing,
// internal/transcript) — that's a different, narrower claim: a unit
// conversion of already-known suppressed bytes into tokens and $ at real
// rates, not a re-measurement of total billed cost, and it carries its own
// caveat/tooltip there. This terminal receipt stays bytes-only on purpose —
// it has no room for that caveat, so it doesn't carry the estimate.
func receiptMessage(s *stats.Session, r stats.Rollup) string {
	var parts []string
	if s.DedupHits > 0 {
		parts = append(parts, fmt.Sprintf("%d repeat command%s deduped (%d bytes not replayed)",
			s.DedupHits, plural(s.DedupHits), s.DedupBytes))
	}
	if s.BudgetTrims > 0 {
		parts = append(parts, fmt.Sprintf("%d long output%s trimmed (%d lines omitted)",
			s.BudgetTrims, plural(s.BudgetTrims), s.BudgetLinesOmitted))
	}
	if s.RetiredCalls > 0 {
		parts = append(parts, fmt.Sprintf("%d investigate call%s retired post-boundary (%d bytes archived)",
			s.RetiredCalls, plural(s.RetiredCalls), s.RetiredBytes))
	}

	return fmt.Sprintf(
		"[agent-winglet] this session: %s. Lifetime across %d session%s: %d dedup hits, %d trims, %d retires. "+
			"(Raw suppressed-content counts, not a validated usage or cost figure.)",
		strings.Join(parts, ", "), r.Sessions, plural(r.Sessions), r.DedupHits, r.BudgetTrims, r.RetiredCalls,
	)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
