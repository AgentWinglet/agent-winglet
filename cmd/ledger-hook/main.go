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
//     and retired-content directory so no substitution, boundary
//     suggestion, or retired receipt survives a restart or compaction (a
//     hard constraint).
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

	"github.com/umitkaanusta/agent-winglet/internal/ledger"
	"github.com/umitkaanusta/agent-winglet/internal/phase"
	"github.com/umitkaanusta/agent-winglet/internal/retire"
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
// reports ok == false when stdout is short enough to leave untouched.
func budgetStdout(stdout string) (budgeted string, ok bool) {
	lines := strings.Split(stdout, "\n")
	trailingNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	if trailingNewline {
		lines = lines[:len(lines)-1]
	}
	n := len(lines)
	if n <= budgetLineThreshold {
		return "", false
	}

	var b strings.Builder
	b.WriteString(strings.Join(lines[:budgetHeadLines], "\n"))
	b.WriteString("\n")
	omitted := n - budgetHeadLines - budgetTailLines
	fmt.Fprintf(&b, "[agent-winglet] %d lines omitted, exit 0 (showing first %d/last %d)\n",
		omitted, budgetHeadLines, budgetTailLines)
	b.WriteString(strings.Join(lines[n-budgetTailLines:], "\n"))
	if trailingNewline {
		b.WriteString("\n")
	}
	return b.String(), true
}

type hookInput struct {
	SessionID     string          `json:"session_id"`
	Cwd           string          `json:"cwd"`
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	ToolResponse  json.RawMessage `json:"tool_response"`
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
		return nil, retire.Invalidate(in.Cwd, in.SessionID)
	case "PostToolUse":
		return handlePostToolUse(in)
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
	receipt := fmt.Sprintf(
		"[agent-winglet] investigate output retired post-boundary (%s %s, %d bytes) — full content at %s",
		in.ToolName, key, len(in.ToolResponse), path,
	)
	return &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     "PostToolUse",
		UpdatedToolOutput: receipt,
	}}, nil
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

	repeatOfTurn, isRepeat := st.Check(key, response.Stdout)
	if !isRepeat {
		if err := ledger.Save(in.Cwd, in.SessionID, st); err != nil {
			return nil, err
		}
		budgeted, ok := budgetStdout(response.Stdout)
		if !ok {
			return nil, nil
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
	return repeatOut, nil
}
