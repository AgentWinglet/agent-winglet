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
//     one-time suggestion to compact (the investigate→implement boundary
//     lever — see handlePhaseBoundary). Anything else passes through
//     untouched.
//   - SessionStart / PostCompact: deletes the session's ledger and phase
//     state so neither a substitution nor a boundary suggestion survives a
//     restart or compaction (a hard constraint).
//
// Read is intentionally not handled by the Ledger (repeat-detection) path:
// Claude Code already detects an unchanged file natively and returns
// tool_response.type == "file_unchanged" on a repeat Read, confirmed by
// inspecting real PostToolUse payloads. Adding ledger logic for Read would
// duplicate a capability the harness already provides for free. Read is
// still used, separately, as an investigate-classified signal for the phase
// boundary.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/umitkaanusta/agent-winglet/internal/ledger"
	"github.com/umitkaanusta/agent-winglet/internal/phase"
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
		return nil, phase.Invalidate(in.Cwd, in.SessionID)
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

// handlePhaseBoundary is the investigate→implement lever from
// agent-winglet-v1-remaining.md §2.1. Claude Code has no hook mechanism to
// trigger compaction programmatically (confirmed against the hooks
// reference: PreCompact can only observe or block a compaction already under
// way), so on the first implement-classified call after at least one
// investigate-classified call this session, it can only suggest — via both
// systemMessage (shown directly to the user) and additionalContext (fed to
// the model, in case the user doesn't notice systemMessage or the agent
// should act on it, e.g. by proposing /compact itself). Fires at most once
// per session (phase.State.Observe's latch), so it never nags on every
// subsequent edit.
func handlePhaseBoundary(in hookInput) (*hookOutput, error) {
	isInvestigate := investigateTools[in.ToolName]
	isImplement := implementTools[in.ToolName]
	if !isInvestigate && !isImplement {
		return nil, nil
	}

	st, err := phase.Load(in.Cwd, in.SessionID)
	if err != nil {
		return nil, err
	}
	crossed := st.Observe(isInvestigate, isImplement)
	if err := phase.Save(in.Cwd, in.SessionID, st); err != nil {
		return nil, err
	}
	if !crossed {
		return nil, nil
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
	}, nil
}

func handlePostToolUse(in hookInput) (*hookOutput, error) {
	if out, err := handlePhaseBoundary(in); out != nil || err != nil {
		return out, err
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

	out := &hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     "PostToolUse",
		UpdatedToolOutput: bashOutput{Stdout: receipt},
	}}
	if err := ledger.Save(in.Cwd, in.SessionID, st); err != nil {
		return nil, err
	}
	return out, nil
}
