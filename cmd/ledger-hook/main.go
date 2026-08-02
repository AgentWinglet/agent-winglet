// ledger-hook is the Claude Code hook binary for the Session Ledger. It is
// registered for two hook events:
//
//   - PostToolUse (matcher: Bash): on an exact-repeat of a previously seen
//     command's output, replaces the output with a compact "unchanged since
//     turn N" reference via updatedToolOutput. First-time output passes
//     through untouched.
//   - SessionStart / PostCompact: deletes the session's ledger file so no
//     substitution ever survives a restart or compaction (a hard constraint).
//
// Read is intentionally not handled here: Claude Code already detects an
// unchanged file natively and returns tool_response.type == "file_unchanged"
// on a repeat Read, confirmed by inspecting real PostToolUse payloads. Adding
// ledger logic for Read would duplicate a capability the harness already
// provides for free.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/umitkaanusta/agent-winglet/internal/ledger"
)

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
}

type hookOutput struct {
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
		return nil, ledger.Invalidate(in.Cwd, in.SessionID)
	case "PostToolUse":
		return handlePostToolUse(in)
	}
	return nil, nil
}

func handlePostToolUse(in hookInput) (*hookOutput, error) {
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
		return nil, ledger.Save(in.Cwd, in.SessionID, st)
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
