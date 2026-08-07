// codex-hook is the Codex hook binary for agent-winglet. In this phase it
// registers projects, resets per-session state at session starts/compacts,
// records Codex rollout-derived usage, and carries a disabled-by-default
// replacement probe. It deliberately does not run real suppression yet; dedup,
// budgeting, and retirement wait for the probe to validate the right Codex
// PostToolUse replacement shape.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/umitkaanusta/agent-winglet/internal/codexrollout"
	"github.com/umitkaanusta/agent-winglet/internal/config"
	"github.com/umitkaanusta/agent-winglet/internal/ledger"
	"github.com/umitkaanusta/agent-winglet/internal/phase"
	"github.com/umitkaanusta/agent-winglet/internal/projectroot"
	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/retire"
	"github.com/umitkaanusta/agent-winglet/internal/stats"
	"github.com/umitkaanusta/agent-winglet/internal/transcript"
)

type hookInput struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	Cwd            string          `json:"cwd"`
	HookEventName  string          `json:"hook_event_name"`
	Model          string          `json:"model"`
	PermissionMode string          `json:"permission_mode"`
	TurnID         string          `json:"turn_id"`
	Source         string          `json:"source"`
	Trigger        string          `json:"trigger"`
	Reason         string          `json:"reason"`
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id"`
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolResponse   json.RawMessage `json:"tool_response"`
	AgentID        string          `json:"agent_id"`
	AgentType      string          `json:"agent_type"`
}

type hookOutput struct {
	Continue           *bool               `json:"continue,omitempty"`
	StopReason         string              `json:"stopReason,omitempty"`
	SystemMessage      string              `json:"systemMessage,omitempty"`
	Decision           string              `json:"decision,omitempty"`
	Reason             string              `json:"reason,omitempty"`
	HookSpecificOutput *hookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext,omitempty"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "codex-hook:", err)
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

func handle(in hookInput) (*hookOutput, error) {
	switch in.HookEventName {
	case "SessionStart", "PostCompact":
		return nil, resetSession(in)
	case "PostToolUse":
		return handlePostToolUse(in)
	case "Stop":
		return nil, recordTranscriptDelta(projectroot.Resolve(in.Cwd), in)
	case "SessionEnd":
		return handleSessionEnd(in)
	}
	return nil, nil
}

func handlePostToolUse(in hookInput) (*hookOutput, error) {
	if err := recordTranscriptDelta(projectroot.Resolve(in.Cwd), in); err != nil {
		return nil, err
	}
	return maybeCodexProbeOutput(in), nil
}

const (
	codexProbeEnvVar        = "AGENT_WINGLET_CODEX_PROBE"
	codexProbeCommandNeedle = "agent-winglet-codex-probe"
)

func maybeCodexProbeOutput(in hookInput) *hookOutput {
	mode := codexProbeMode()
	if mode == "" || in.HookEventName != "PostToolUse" || in.ToolName != "Bash" {
		return nil
	}

	var input struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal(in.ToolInput, &input); err != nil {
		return nil
	}
	if !strings.Contains(input.Command, codexProbeCommandNeedle) {
		return nil
	}

	receipt := fmt.Sprintf("[agent-winglet] codex replacement probe (%s) replaced Bash output for %q", mode, input.Command)
	if mode == "block" {
		return &hookOutput{
			Decision: "block",
			Reason:   receipt,
			HookSpecificOutput: &hookSpecificOutput{
				HookEventName:     "PostToolUse",
				AdditionalContext: receipt,
			},
		}
	}

	keepGoing := false
	return &hookOutput{
		Continue:      &keepGoing,
		StopReason:    receipt,
		SystemMessage: receipt,
	}
}

func codexProbeMode() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(codexProbeEnvVar))) {
	case "1", "true", "continue", "continue-false":
		return "continue"
	case "block", "decision-block":
		return "block"
	default:
		return ""
	}
}

func resetSession(in hookInput) error {
	root := projectroot.Resolve(in.Cwd)
	if err := ledger.Invalidate(root, in.SessionID); err != nil {
		return err
	}
	if err := phase.Invalidate(root, in.SessionID); err != nil {
		return err
	}
	if err := retire.Invalidate(root, in.SessionID); err != nil {
		return err
	}
	if err := stats.InvalidateSession(root, in.SessionID); err != nil {
		return err
	}
	if err := ensureCodexAgentForExistingUsage(root, in.SessionID); err != nil {
		return err
	}
	return registry.Register(root)
}

func ensureCodexAgentForExistingUsage(projectDir, sessionID string) error {
	s, err := stats.LoadSession(projectDir, sessionID)
	if err != nil {
		return err
	}
	if s.TranscriptTokens == 0 && s.TranscriptCostUSD == 0 && s.TranscriptContentBytes == 0 && s.TranscriptOffset == 0 {
		return nil
	}
	s.Agent = stats.AgentCodex
	return stats.SaveSession(projectDir, sessionID, s)
}

func recordTranscriptDelta(projectDir string, in hookInput) error {
	s, err := stats.LoadSession(projectDir, in.SessionID)
	if err != nil {
		return err
	}
	previous := transcript.SessionUsage{
		Tokens:       s.TranscriptTokens,
		CostUSD:      s.TranscriptCostUSD,
		ContentBytes: s.TranscriptContentBytes,
	}
	delta, newOffset, err := codexrollout.ReadSessionUsageFrom(in.TranscriptPath, s.TranscriptOffset, previous)
	if err != nil {
		return err
	}
	if newOffset == s.TranscriptOffset {
		return nil
	}
	s.Agent = stats.AgentCodex
	s.AddTranscriptUsage(delta, newOffset)
	return stats.SaveSession(projectDir, in.SessionID, s)
}

const quietEnvVar = "AGENT_WINGLET_QUIET"

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

func handleSessionEnd(in hookInput) (*hookOutput, error) {
	root := projectroot.Resolve(in.Cwd)

	sess, err := stats.LoadSession(root, in.SessionID)
	if err != nil {
		return nil, err
	}
	sess.Agent = stats.AgentCodex
	hadMechanismActivity := !sess.IsZero()

	if usage, offset, err := codexrollout.ReadSessionUsageWithOffset(in.TranscriptPath); err == nil {
		sess.SetTranscriptUsage(usage, offset)
	}

	if !hadMechanismActivity && sess.TranscriptContentBytes == 0 {
		return nil, nil
	}
	if err := stats.SaveSession(root, in.SessionID, sess); err != nil {
		return nil, err
	}
	if quiet() || !hadMechanismActivity {
		return nil, nil
	}

	rollup, err := stats.SumProject(root)
	if err != nil {
		return nil, err
	}
	return &hookOutput{SystemMessage: receiptMessage(sess, rollup)}, nil
}

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
