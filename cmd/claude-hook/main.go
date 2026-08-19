// claude-hook is the Claude Code hook binary for agent-winglet. It is
// registered for four hook events:
//
//   - PostToolUse (all tools, no matcher): on Bash, an exact repeat of a
//     previously seen command's output is replaced with a compact
//     "unchanged since turn N" reference; a first-time, successful command
//     with long stdout is replaced with a head/tail receipt instead
//     (output budgeting). Pre-boundary, Grep's content field and Glob's
//     filenames array get the same budgeting once they cross the
//     token-count threshold (handleGrepBudget/handleGlobBudget) —
//     WebFetch, WebSearch, and Read are deliberately excluded, see
//     handlePostToolUse's default case for why. On the first
//     implement-classified call (Edit/Write/NotebookEdit) after at least
//     one investigate-classified call (Read/Grep/Glob/WebFetch/WebSearch/
//     Task) this session, emits a one-time suggestion to compact
//     (handlePhaseBoundary). Once that boundary is crossed, any further
//     long investigate call has its output archived to disk and replaced with a
//     receipt (handleRetireInvestigate) instead of budgeted; the same
//     archive-and-receipt treatment also fires pre-boundary once a session
//     passes investigateCallThreshold investigate calls, since a long
//     investigation otherwise accumulates many individually-small outputs
//     that never trip Grep/Glob's per-call budgeting on their own. Only
//     calls made after a boundary/threshold crossing can be affected — a
//     PostToolUse hook can never rewrite a call already delivered. Bash
//     gets the same post-boundary archive-and-receipt treatment
//     (handleBashRetire) even though it's never investigate-classified;
//     everything else passes through untouched.
//   - SessionStart / PostCompact: wipes the session's ledger, phase state,
//     and retired-content directory (the state used to detect future
//     repeats/boundaries), so nothing from a part of the context that's now
//     gone misfires against new content. The stats tally is deliberately
//     left alone — dedup/budget/retire savings already happened, and a
//     restart or compaction doesn't undo bytes that were genuinely kept out
//     of the model's context, so a session_id's receipt keeps accumulating
//     across a compact/resume instead of resetting. Also registers the
//     current project (by git root, via projectroot.Resolve, so sessions
//     started from different subdirectories collapse into one identity) in
//     the global ~/.agent-winglet/projects.json registry, and best-effort
//     migrates any pre-move-storage per-cwd state into that project's new
//     state dir (migrateLegacyData).
//   - Stop: records the transcript-usage delta since the last recorded
//     offset (recordTranscriptDelta), the same call PostToolUse makes, so a
//     session that never calls a tool — pure chat — still gets a stats file
//     written after its first turn instead of only at SessionEnd, and isn't
//     silently dropped from every rollup if the process dies before then.
//   - SessionEnd: emits a one-time "savings receipt" systemMessage
//     summarizing what the above mechanisms did this session plus the
//     project's lifetime total, computed fresh across every session file on
//     disk rather than a separately maintained running total (see
//     stats.SumProject and handleSessionEnd). Emits nothing if no mechanism
//     fired this session or if AGENT_WINGLET_QUIET is set.
//
// Read is intentionally not handled by the ledger's repeat-detection path:
// Claude Code already detects an unchanged file natively and returns
// tool_response.type == "file_unchanged" on a repeat Read, confirmed by
// inspecting real PostToolUse payloads — adding ledger logic for Read would
// duplicate a capability the CLI already provides for free. Read is still
// used, separately, as an investigate-classified signal for the phase
// boundary and for post-boundary retirement.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AgentWinglet/agent-winglet/internal/compactnudge"
	"github.com/AgentWinglet/agent-winglet/internal/config"
	"github.com/AgentWinglet/agent-winglet/internal/entitlement"
	"github.com/AgentWinglet/agent-winglet/internal/ledger"
	"github.com/AgentWinglet/agent-winglet/internal/outputbudget"
	"github.com/AgentWinglet/agent-winglet/internal/phase"
	"github.com/AgentWinglet/agent-winglet/internal/projectroot"
	"github.com/AgentWinglet/agent-winglet/internal/registry"
	"github.com/AgentWinglet/agent-winglet/internal/retire"
	"github.com/AgentWinglet/agent-winglet/internal/statedir"
	"github.com/AgentWinglet/agent-winglet/internal/stats"
	"github.com/AgentWinglet/agent-winglet/internal/transcript"
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
	budgetTokenThreshold = outputbudget.TokenThreshold
	budgetHeadLines      = outputbudget.HeadLines
	budgetTailLines      = outputbudget.TailLines
)

func estimatedTokens(body string) int {
	return outputbudget.EstimatedTokens(body)
}

func budgetBody(body, root, sessionID string, notice func(omitted int, archivePath string) string) (budgeted string, omittedLines int, omittedBytes int64, ok bool, err error) {
	return outputbudget.Body(body, root, sessionID, notice)
}

func budgetStdout(stdout, root, sessionID string) (budgeted string, omittedLines int, omittedBytes int64, ok bool, err error) {
	return outputbudget.Stdout(stdout, root, sessionID)
}

func budgetTextField(body, root, sessionID string) (budgeted string, omittedLines int, omittedBytes int64, ok bool, err error) {
	return outputbudget.TextField(body, root, sessionID)
}

func budgetEntryList(entries []string, root, sessionID string) (budgeted string, omittedEntries int, omittedBytes int64, ok bool, err error) {
	return outputbudget.EntryList(entries, root, sessionID)
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
		fmt.Fprintln(os.Stderr, "claude-hook:", err)
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
	if result := entitlement.Check(entitlement.FeatureHookSavings, time.Now()); !result.Allowed {
		return entitlementBlockedOutput(result, "claude", in.SessionID, in.HookEventName), nil
	}
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
		// Note: stats' per-session tally is deliberately left untouched here.
		// Dedup/budget-trim/retire savings already happened — real bytes that
		// were genuinely kept out of the model's context — and a compact or
		// resume doesn't undo that, so the receipt for this session_id should
		// keep accumulating across it. Only ledger/phase/retire's own
		// operational state (which detects future repeats/boundaries against
		// content that's now gone from context) needs resetting.
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

// entitlementBlockedOutput always short-circuits handle's business logic for
// a session that isn't entitled — every hook event takes this path, not just
// the first one. entitlement.ShouldEmitNotice separately throttles the
// *visible* notice to once per session so it doesn't repeat on every tool
// call; conflating that throttle with the gate itself (both driven off the
// same nil/non-nil return) was the bug — only the first blocked call in a
// session actually skipped the suppression logic below, and every later
// call ran it in full because ShouldEmitNotice's second call returned false.
//
// The one notice a session gets is deliberately louder than a line the model
// might fold silently into whatever else it's doing: additionalContext
// reaches the model (unlike systemMessage, which the user already sees on
// its own — same split this file's compact nudge uses), so it explicitly
// instructs Claude to check in with the user via AskUserQuestion before
// continuing, rather than just mentioning Winglet is inactive and hoping
// that lands. This only fires once per session, not on every blocked call —
// interrupting every single tool call to nag about signing in would make
// Winglet feel predatory rather than just gated.
func entitlementBlockedOutput(result entitlement.CheckResult, agent, sessionID, eventName string) *hookOutput {
	out := &hookOutput{HookSpecificOutput: hookSpecificOutput{HookEventName: eventName}}
	if !entitlement.ShouldEmitNotice(agent, sessionID) {
		return out
	}
	msg := entitlement.NoticeFor(result.Reason)
	out.SystemMessage = msg
	action := entitlement.ActionFor(result.Reason)
	out.HookSpecificOutput.AdditionalContext = msg + " Before continuing with any further " +
		"work, ask the user now via the AskUserQuestion tool whether they'd like to " +
		action + " now or continue this session without Winglet active."
	return out
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
// sum-of-session-files model, folding their counts onto the legacy-migrated
// seed session (see legacySessionID) rather than a second persisted ledger:
// a pre-move-storage install's per-project state
// (<cwd>/.claude/agent-winglet/lifetime.stats.json — only the lifetime
// figures have lasting value, everything else there is session-scoped and
// never survives a restart anyway), and this project's own current-layout
// lifetime.stats.json from before the sum-of-session-files switch. Runs on
// every SessionStart/PostCompact so a stale per-subdirectory dir from the
// old cwd-keyed layout gets folded in whenever a session next starts from
// it; each fold reads-mutates-saves the same seed session
// (mergeLegacyLifetime), so repeated calls never double-count.
//
// Best-effort: errors are logged to stderr, never surfaced to the hook's
// caller, matching this codebase's fail-soft convention for state I/O.
func migrateLegacyData(cwd, root string) {
	legacyDir := filepath.Join(cwd, ".claude", "agent-winglet")
	if info, err := os.Stat(legacyDir); err == nil && info.IsDir() {
		mergeLegacyLifetime(root, filepath.Join(legacyDir, stats.LifetimeFileName))
		if err := os.RemoveAll(legacyDir); err != nil {
			fmt.Fprintln(os.Stderr, "claude-hook: migration: removing legacy dir failed:", err)
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
		fmt.Fprintln(os.Stderr, "claude-hook: migration: corrupt legacy lifetime stats, skipping merge:", err)
		return
	}

	seed, err := stats.LoadSession(root, legacySessionID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "claude-hook: migration: loading seed session failed:", err)
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
		fmt.Fprintln(os.Stderr, "claude-hook: migration: saving seed session failed:", err)
		return
	}
	if err := os.Remove(path); err != nil {
		fmt.Fprintln(os.Stderr, "claude-hook: migration: removing legacy lifetime file failed:", err)
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

// investigateCallThreshold pre-emptively retires investigate-classified
// output once a session has made more investigate calls than this, even
// before the investigate→implement boundary crosses (see
// phase.State.InvestigateCalls). Pre-boundary, an investigate call
// otherwise gets exactly one shot at reduction — Grep/Glob's per-call
// budgeting, which only trims a single call once it individually exceeds
// budgetTokenThreshold and never revisits it. That leaves a gap: many
// individually-small investigate calls can still accumulate unboundedly
// over a long investigation. This closes it — past the threshold, every
// further investigate call is retired outright (archive + receipt, the
// same treatment handleRetireInvestigate gives post-boundary calls).
//
// Only affects calls made after the threshold is crossed — a PostToolUse
// hook can never rewrite a call already delivered, so the first
// investigateCallThreshold calls stay in context exactly as they arrived.
// That makes this a threshold ("prefix stays, tail retires"), not the true
// sliding window either paper describes (Complexity Trap's M, AgentDiet's
// a) — this architecture has no hook to retroactively mask a call already
// replayed N turns ago. Picked generously rather than derived from either
// paper's window size; tune down only if dogfooding shows 20 is too late
// to matter.
const investigateCallThreshold = phase.InvestigateCallThreshold

// handlePhaseBoundary suggests running /compact once the session has moved
// from investigating to implementing. Claude Code has no hook mechanism to
// trigger compaction programmatically (PreCompact can only observe or block
// a compaction already under way), so on the first implement-classified
// call after at least one investigate-classified call this session, it can
// only suggest — via both systemMessage (shown to the user) and
// additionalContext (fed to the model, in case it should act on it, e.g.
// by proposing /compact itself). Fires at most once per session
// (phase.State.Observe's latch).
//
// Also reports pastBoundary (has the session already crossed the boundary
// as of this call) and overInvestigateThreshold (did this call's own tally
// push phase.State.InvestigateCalls past investigateCallThreshold) —
// handlePostToolUse uses both to decide whether to retire a later
// investigate call's output (handleRetireInvestigate). Only an investigate
// call made after one of those crossings can be retired; a PostToolUse
// hook can never rewrite a call already delivered.
func handlePhaseBoundary(in hookInput, root string) (out *hookOutput, pastBoundary bool, overInvestigateThreshold bool, err error) {
	isInvestigate := investigateTools[in.ToolName]
	isImplement := implementTools[in.ToolName]

	st, err := phase.Load(root, in.SessionID)
	if err != nil {
		return nil, false, false, err
	}
	if !isInvestigate && !isImplement {
		// Unclassified tools (e.g. Bash — see investigateTools' doc comment
		// on why it's deliberately left out of both maps) never advance
		// phase state, but still need an accurate pastBoundary read: Bash's
		// own post-boundary retirement path (handleBashRetire) depends on
		// it, even though Bash itself never crosses or is retired by this
		// function.
		return nil, st.Suggested, st.InvestigateCalls > investigateCallThreshold, nil
	}
	crossed := st.Observe(isInvestigate, isImplement)
	if err := phase.Save(root, in.SessionID, st); err != nil {
		return nil, false, false, err
	}
	pastBoundary = st.Suggested
	overInvestigateThreshold = st.InvestigateCalls > investigateCallThreshold
	if !crossed {
		return nil, pastBoundary, overInvestigateThreshold, nil
	}

	if cfg, err := config.Load(); err == nil && cfg.CompactNudgeDisabled {
		return nil, pastBoundary, overInvestigateThreshold, nil
	}

	msg := compactnudge.Message
	// additionalContext reaches the model, not the user directly (unlike
	// systemMessage, which the user already sees on its own) — so it spells
	// out that the model must act on this now, before any further tool
	// calls, rather than silently folding it into whatever else it's
	// already doing. Directing it at the AskUserQuestion tool specifically
	// (rather than just "tell the user") turns this from text the user
	// might skim past into an explicit decision they have to make one way
	// or the other.
	modelInstruction := msg + " Before continuing with any further " +
		"work, ask the user now via the AskUserQuestion tool whether " +
		"they'd like to run /compact now." + compactnudge.PreservationGuidance

	return &hookOutput{
		SystemMessage: msg,
		HookSpecificOutput: hookSpecificOutput{
			HookEventName:     "PostToolUse",
			AdditionalContext: modelInstruction,
		},
	}, pastBoundary, overInvestigateThreshold, nil
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

// investigateThresholdReason is handleRetireInvestigate's reason string for
// a pre-boundary retirement triggered by investigateCallThreshold, as
// opposed to the post-boundary case (reason "post-boundary").
const investigateThresholdReason = "past the pre-boundary investigate-call threshold"

// handleRetireInvestigate retires used-up investigate output: an
// investigate-classified call's own output — the one thing a PostToolUse
// hook can still rewrite — is archived to disk and replaced with a compact
// receipt, instead of replaying it in full. Called in two situations (see
// handlePostToolUse): once the session has already crossed the
// investigate→implement boundary (see handlePhaseBoundary), or, pre-
// boundary, once investigateCallThreshold investigate calls have already
// been made this session. The post-boundary path is length-gated so short,
// immediately useful observations still pass through. reason names which
// condition fired, and is threaded straight into the receipt so a saved
// output's notice line explains why it was cut.
func handleRetireInvestigate(in hookInput, root, reason string) (*hookOutput, error) {
	path, err := retire.Store(root, in.SessionID, in.ToolResponse)
	if err != nil {
		return nil, err
	}
	key := investigateKey(in.ToolInput)
	n := len(in.ToolResponse)
	receipt := fmt.Sprintf(
		"[agent-winglet] investigate output retired %s (%s %s, %d bytes) — full content at %s",
		reason, in.ToolName, key, n, path,
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

	out, pastBoundary, overInvestigateThreshold, err := handlePhaseBoundary(in, root)
	if err != nil {
		return nil, err
	}
	if out != nil {
		return out, nil
	}
	if investigateTools[in.ToolName] {
		if pastBoundary {
			if estimatedTokens(string(in.ToolResponse)) > budgetTokenThreshold {
				return handleRetireInvestigate(in, root, "post-boundary")
			}
			return nil, nil
		}
		if overInvestigateThreshold {
			return handleRetireInvestigate(in, root, investigateThresholdReason)
		}
	}

	// Everything past this point means neither retirement condition fired
	// for this call: for investigate-classified tools, the boundary hasn't
	// crossed yet and this session hasn't made more than
	// investigateCallThreshold investigate calls yet either. Bash is the one
	// exception — it's never investigate-classified (see investigateTools'
	// doc comment), so it never goes through handleRetireInvestigate above,
	// but it still needs pastBoundary to pick its own post-boundary
	// treatment (retire long output instead of just budgeting it — see
	// handleBashPostToolUse). Budgeting and retirement are still mutually
	// exclusive per call: pastBoundary routes Bash to one or the other,
	// never both.
	switch in.ToolName {
	case "Bash":
		return handleBashPostToolUse(in, root, pastBoundary)
	case "Grep":
		return handleGrepBudget(in, root)
	case "Glob":
		return handleGlobBudget(in, root)
	default:
		// Notably absent, all deliberate scope cuts:
		//
		// WebFetch's result isn't raw page content — it's already fetched,
		// converted to markdown, and answered by a small model against the
		// caller's prompt, and self-summarizes when the source is huge. It's
		// rarely long enough to need budgeting, and when it is, that's
		// because the prompt asked for something extensive — truncating it
		// would cut the requested content itself, not waste.
		//
		// WebSearch's tool_response is a heterogeneous results array with no
		// single freeform field budgetBody can act on, and result counts are
		// already small in practice.
		//
		// Read has no confirmed tool_response schema at all (unlike
		// Grep/Glob) — guessing a file-content field to truncate risks
		// silently corrupting real file content, worse than leaving it
		// untouched.
		return nil, nil
	}
}

// handleBashPostToolUse implements the ledger's Bash-specific path: repeat
// detection (dedup) first, then — for first-time calls that don't hit the
// ledger — either budgeting or retirement of long output, depending on
// pastBoundary. Bash is not investigate-classified (see investigateTools'
// doc comment on why it's deliberately left unclassified), so it never goes
// through handleRetireInvestigate; pastBoundary is threaded in here
// specifically so Bash can still get retirement's stronger recovery
// guarantee post-boundary without joining that tool_name classification
// (see handleBashRetire).
func handleBashPostToolUse(in hookInput, root string, pastBoundary bool) (*hookOutput, error) {
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
		if pastBoundary {
			return handleBashRetire(in, root, response.Stdout, input.Command)
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

// handleBashRetire is handleBashPostToolUse's post-boundary counterpart to
// budgetStdout: once the session has crossed the investigate→implement
// boundary, a first-time Bash call's long stdout is archived to disk in
// full via retire.Store and replaced with a compact receipt — the same
// recovery guarantee handleRetireInvestigate gives investigate-classified
// tools — instead of just keeping a head/tail slice visible. Gating on
// pastBoundary here, rather than folding Bash into investigateTools, keeps
// that classification's boundary-detection role untouched.
//
// Uses the same "is this worth touching" gate budgetStdout does
// (estimatedTokens vs. budgetTokenThreshold), so a short success message
// doesn't need archiving even post-boundary.
func handleBashRetire(in hookInput, root, stdout, command string) (*hookOutput, error) {
	if estimatedTokens(stdout) <= budgetTokenThreshold {
		return nil, nil
	}

	path, err := retire.Store(root, in.SessionID, []byte(stdout))
	if err != nil {
		return nil, err
	}
	n := len(stdout)
	receipt := fmt.Sprintf(
		"[agent-winglet] bash output retired post-boundary (%s, %d bytes) — full output at %s",
		command, n, path,
	)
	if err := recordStat(root, in.SessionID, func(s *stats.Session) {
		s.RecordRetire(n)
	}); err != nil {
		return nil, err
	}
	return &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     "PostToolUse",
		UpdatedToolOutput: bashOutput{Stdout: receipt},
	}}, nil
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
// savings figure, since no measurement of *total session cost* exists (a
// paired-run test came back inconclusive). That framing is load-bearing: a
// self-reported "this saved you money" claim with no evidence is the exact
// failure mode this receipt exists to avoid.
//
// The desktop app's Overview screen does surface a priced dollar estimate
// (stats.Session.TranscriptCostUSD) — a narrower claim, a unit conversion
// of already-known suppressed bytes into tokens/$ at real rates, not a
// re-measurement of total billed cost, and it carries its own caveat there.
// This terminal receipt has no room for that caveat, so it stays
// bytes-only.
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
