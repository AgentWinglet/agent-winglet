// ledger-hook is the Claude Code hook binary for agent-winglet. It is
// registered for two hook events:
//
//   - PostToolUse (all tools — see settings.json/install.sh, no matcher):
//     on Bash, an exact-repeat of a previously seen command's output
//     replaces the output with a compact "unchanged since turn N" reference
//     via updatedToolOutput; a first-time, successful command whose stdout
//     is long is replaced with a head/tail receipt (output budgeting by
//     outcome). On the first implement-classified call (Edit/Write/
//     NotebookEdit) after at least one investigate-classified call
//     (Read/Grep/Glob/WebFetch/WebSearch/Task) this session, emits a
//     one-time suggestion to compact (see handlePhaseBoundary). Once that
//     boundary has already been crossed, any further investigate-classified
//     call has its own output archived to disk and replaced with a compact
//     receipt (see handleRetireInvestigate). Anything else passes through
//     untouched.
//   - SessionStart / PostCompact: deletes the session's ledger, phase state,
//     retired-content directory, and stats tally so no substitution,
//     boundary suggestion, retired receipt, or session receipt survives a
//     restart or compaction (a hard constraint). Also registers the current
//     project (in.Cwd) in the global ~/.agent-winglet/projects.json registry
//     (see internal/registry.Register) — the hook installs globally now, so
//     this is the only place a project gets added to that registry.
//   - SessionEnd: emits a one-time "savings receipt" systemMessage
//     summarizing what the above mechanisms did this session (see
//     handleSessionEnd), then folds the session's tally into a lifetime
//     total. SessionEnd was confirmed to support the systemMessage output
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
	"strings"

	"github.com/umitkaanusta/agent-winglet/internal/config"
	"github.com/umitkaanusta/agent-winglet/internal/ledger"
	"github.com/umitkaanusta/agent-winglet/internal/phase"
	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/retire"
	"github.com/umitkaanusta/agent-winglet/internal/stats"
	"github.com/umitkaanusta/agent-winglet/internal/transcript"
)

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

// budgetStdout collapses stdout to its first budgetHeadLines and last
// budgetTailLines lines if it has more than budgetLineThreshold lines. It
// reports ok == false when stdout is short enough to leave untouched, and
// omittedLines/omittedBytes as the size of the dropped middle section (both
// 0 when ok is false). omittedBytes is the "\n"-joined byte length of just
// the dropped lines — the same original-size unit RecordDedup/RecordRetire
// use — so budget-trims can contribute to the same suppressed-bytes total.
func budgetStdout(stdout string) (budgeted string, omittedLines int, omittedBytes int64, ok bool) {
	lines := strings.Split(stdout, "\n")
	trailingNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	n := len(lines)
	if n <= budgetLineThreshold {
		return "", 0, 0, false
	}

	dropped := lines[budgetHeadLines : n-budgetTailLines]
	omittedLines = len(dropped)
	omittedBytes = int64(len(strings.Join(dropped, "\n")))

	var b strings.Builder
	b.WriteString(strings.Join(lines[:budgetHeadLines], "\n"))
	b.WriteString("\n")
	fmt.Fprintf(&b, "[agent-winglet] %d lines omitted, exit 0 (showing first %d/last %d)\n",
		omittedLines, budgetHeadLines, budgetTailLines)
	b.WriteString(strings.Join(lines[n-budgetTailLines:], "\n"))
	if trailingNewline {
		b.WriteString("\n")
	}
	return b.String(), omittedLines, omittedBytes, true
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
		if err := ledger.Invalidate(in.Cwd, in.SessionID); err != nil {
			return nil, err
		}
		if err := phase.Invalidate(in.Cwd, in.SessionID); err != nil {
			return nil, err
		}
		if err := retire.Invalidate(in.Cwd, in.SessionID); err != nil {
			return nil, err
		}
		if err := stats.InvalidateSession(in.Cwd, in.SessionID); err != nil {
			return nil, err
		}
		// Self-registration: now that the hook installs globally rather than
		// per-project (see install.sh), there's no install-time moment inside
		// each project to register it in ~/.agent-winglet/projects.json.
		// Registering here means the first session the hook ever sees for a
		// given cwd adds that project to the registry.
		return nil, registry.Register(in.Cwd)
	case "PostToolUse":
		return handlePostToolUse(in)
	case "SessionEnd":
		return handleSessionEnd(in)
	}
	return nil, nil
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
func handlePhaseBoundary(in hookInput) (out *hookOutput, pastBoundary bool, err error) {
	isInvestigate := investigateTools[in.ToolName]
	isImplement := implementTools[in.ToolName]
	if !isInvestigate && !isImplement {
		return nil, false, nil
	}

	st, err := phase.Load(in.Cwd, in.SessionID)
	if err != nil {
		return nil, false, err
	}
	crossed := st.Observe(isInvestigate, isImplement)
	if err := phase.Save(in.Cwd, in.SessionID, st); err != nil {
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
func handleRetireInvestigate(in hookInput) (*hookOutput, error) {
	path, err := retire.Store(in.Cwd, in.SessionID, in.ToolResponse)
	if err != nil {
		return nil, err
	}
	key := investigateKey(in.ToolInput)
	n := len(in.ToolResponse)
	receipt := fmt.Sprintf(
		"[agent-winglet] investigate output retired post-boundary (%s %s, %d bytes) — full content at %s",
		in.ToolName, key, n, path,
	)
	if err := recordStat(in.Cwd, in.SessionID, func(s *stats.Session) {
		s.RecordProcessed(n)
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
func recordStat(cwd, sessionID string, mutate func(*stats.Session)) error {
	s, err := stats.LoadSession(cwd, sessionID)
	if err != nil {
		return err
	}
	mutate(s)
	return stats.SaveSession(cwd, sessionID, s)
}

func handlePostToolUse(in hookInput) (*hookOutput, error) {
	out, pastBoundary, err := handlePhaseBoundary(in)
	if err != nil {
		return nil, err
	}
	if out != nil {
		return out, nil
	}
	if investigateTools[in.ToolName] && pastBoundary {
		return handleRetireInvestigate(in)
	}

	if in.ToolName != "Bash" {
		return nil, nil
	}

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

	st, err := ledger.Load(in.Cwd, in.SessionID)
	if err != nil {
		return nil, err
	}

	processedBytes := len(response.Stdout)

	repeatOfTurn, isRepeat := st.Check(key, response.Stdout)
	if !isRepeat {
		if err := ledger.Save(in.Cwd, in.SessionID, st); err != nil {
			return nil, err
		}
		budgeted, omittedLines, omittedBytes, ok := budgetStdout(response.Stdout)
		if !ok {
			// Short enough to leave untouched, but still a call the hook
			// evaluated for suppression — counts toward the denominator.
			if err := recordStat(in.Cwd, in.SessionID, func(s *stats.Session) { s.RecordProcessed(processedBytes) }); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if err := recordStat(in.Cwd, in.SessionID, func(s *stats.Session) {
			s.RecordProcessed(processedBytes)
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
	if err := ledger.Save(in.Cwd, in.SessionID, st); err != nil {
		return nil, err
	}
	if err := recordStat(in.Cwd, in.SessionID, func(s *stats.Session) {
		s.RecordProcessed(processedBytes)
		s.RecordDedup(processedBytes)
	}); err != nil {
		return nil, err
	}
	return repeatOut, nil
}

// quietEnvVar is the one piece of configurability in scope for the savings
// receipt: it suppresses the SessionEnd systemMessage while leaving every
// underlying mechanism (dedup, budgeting, retirement, the compact nudge)
// fully active, and while lifetime bookkeeping still happens.
const quietEnvVar = "AGENT_WINGLET_QUIET"

// quiet checks AGENT_WINGLET_QUIET first, falling back to the global
// ~/.agent-winglet/config.json Quiet field only when the env var is unset
// entirely. The env var wins whenever it's set (including explicitly to "0"
// or "false") so existing terminal-session usage is unaffected by a GUI
// toggle — the config file exists for the desktop app's Settings screen,
// which has no way to set an env var in a Claude Code session's process
// tree (different process, no shared env).
func quiet() bool {
	if v := os.Getenv(quietEnvVar); v != "" {
		return v != "0" && v != "false"
	}
	cfg, err := config.Load()
	if err != nil {
		return false
	}
	return cfg.Quiet
}

// handleSessionEnd emits the session's savings receipt: a summary of what
// the ledger (dedup), output-budgeting, and retirement mechanisms did this
// session, plus the running lifetime total. If no mechanism fired this
// session, it emits nothing — a receipt reporting "0 things happened" is
// noise, not signal (see the package doc's zero-activity note). The
// session's tally is folded into the lifetime tally either way once it's
// non-zero, even when AGENT_WINGLET_QUIET suppresses the message itself.
func handleSessionEnd(in hookInput) (*hookOutput, error) {
	sess, err := stats.LoadSession(in.Cwd, in.SessionID)
	if err != nil {
		return nil, err
	}
	if sess.IsZero() {
		return nil, nil
	}

	// Read errors are swallowed (fields stay zero) — the transcript is real
	// usage data if the read succeeds, and a "no data yet" fallback exists
	// downstream (see stats.Session.TranscriptTokens' doc comment) for
	// whenever it doesn't.
	if usage, err := transcript.ReadSessionUsage(in.TranscriptPath); err == nil {
		sess.SetTranscriptUsage(usage)
	}

	lifetime, err := stats.LoadLifetime(in.Cwd)
	if err != nil {
		return nil, err
	}
	lifetime.Add(sess)
	if err := stats.SaveLifetime(in.Cwd, lifetime); err != nil {
		return nil, err
	}

	if quiet() {
		return nil, nil
	}

	return &hookOutput{
		SystemMessage: receiptMessage(sess, lifetime),
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
func receiptMessage(s *stats.Session, l *stats.Lifetime) string {
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
		strings.Join(parts, ", "), l.Sessions, plural(l.Sessions), l.DedupHits, l.BudgetTrims, l.RetiredCalls,
	)
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
