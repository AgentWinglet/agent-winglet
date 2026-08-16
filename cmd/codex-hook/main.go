// codex-hook is the Codex hook binary for agent-winglet. In this phase it
// registers projects, resets per-session state at session starts/compacts,
// records Codex rollout-derived usage, dedups repeated successful shell output,
// budgets long first-time shell output, retires post-boundary investigation
// output, emits the compact nudge, and carries a disabled-by-default
// replacement probe.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/umitkaanusta/agent-winglet/internal/cmdclass"
	"github.com/umitkaanusta/agent-winglet/internal/codexrollout"
	"github.com/umitkaanusta/agent-winglet/internal/config"
	"github.com/umitkaanusta/agent-winglet/internal/entitlement"
	"github.com/umitkaanusta/agent-winglet/internal/ledger"
	"github.com/umitkaanusta/agent-winglet/internal/outputbudget"
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
	if result := entitlement.Check(entitlement.FeatureHookSavings, time.Now()); !result.Allowed {
		return entitlementBlockedOutput(result, "codex", in.SessionID, in.HookEventName), nil
	}
	switch in.HookEventName {
	case "SessionStart", "PostCompact":
		return nil, resetSession(in)
	case "SubagentStart", "SubagentStop":
		_, err := observeCodexPhase(projectroot.Resolve(in.Cwd), in.SessionID, true, false)
		return nil, err
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
// Codex has no reliable AskUserQuestion equivalent available outside Plan
// mode, so unlike claude-hook's version of this function, the instruction
// below asks the model to raise this with the user directly (same "tell the
// user" phrasing this file's compact nudge already uses for Codex) rather
// than naming a specific tool that may not exist in the current session.
func entitlementBlockedOutput(result entitlement.CheckResult, agent, sessionID, eventName string) *hookOutput {
	if !entitlement.ShouldEmitNotice(agent, sessionID) {
		return &hookOutput{}
	}
	msg := entitlement.NoticeFor(result.Reason)
	return &hookOutput{
		SystemMessage: msg,
		HookSpecificOutput: &hookSpecificOutput{
			HookEventName: eventName,
			AdditionalContext: msg + " Before continuing with any further work, ask the " +
				"user directly whether they'd like to take care of that now or continue " +
				"this session without Winglet active.",
		},
	}
}

func handlePostToolUse(in hookInput) (*hookOutput, error) {
	root := projectroot.Resolve(in.Cwd)
	if err := recordTranscriptDelta(root, in); err != nil {
		return nil, err
	}
	if out := maybeCodexProbeOutput(in); out != nil {
		return out, nil
	}
	class := codexPhaseClass(in)
	crossed, err := observeCodexPhase(root, in.SessionID, class == cmdclass.Investigate, class == cmdclass.Implement)
	if err != nil {
		return nil, err
	}
	if crossed {
		return codexCompactNudgeOutput(), nil
	}
	return handleCodexToolPostUse(in, root, class)
}

const (
	codexProbeEnvVar        = "AGENT_WINGLET_CODEX_PROBE"
	codexProbeCommandNeedle = "agent-winglet-codex-probe"
)

func maybeCodexProbeOutput(in hookInput) *hookOutput {
	mode := codexProbeMode()
	if mode == "" || in.HookEventName != "PostToolUse" || !codexShellTool(in.ToolName) {
		return nil
	}

	command, ok := codexShellCommand(in.ToolInput)
	if !ok || !strings.Contains(command, codexProbeCommandNeedle) {
		return nil
	}

	receipt := fmt.Sprintf("[agent-winglet] codex replacement probe (%s) replaced Bash output for %q", mode, command)
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

	return codexReplacementOutput(receipt)
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

const investigateThresholdReason = "past the pre-boundary investigate-call threshold"

type codexToolOutput struct {
	Key          string
	Text         string
	Shell        bool
	CanRewrite   bool
	RetireLabel  string
	BudgetRetire string
}

func handleCodexToolPostUse(in hookInput, root string, class cmdclass.Class) (*hookOutput, error) {
	toolOutput, ok := codexNormalizeToolOutput(in, class)
	if !ok || !toolOutput.CanRewrite {
		return nil, nil
	}

	st, err := ledger.Load(root, in.SessionID)
	if err != nil {
		return nil, err
	}
	repeatOfTurn, isRepeat := st.Check(toolOutput.Key, toolOutput.Text)
	if !isRepeat {
		if err := ledger.Save(root, in.SessionID, st); err != nil {
			return nil, err
		}
		pastBoundary, overInvestigateThreshold, err := codexPhaseStatus(root, in.SessionID)
		if err != nil {
			return nil, err
		}
		if class == cmdclass.Investigate {
			if pastBoundary {
				return handleCodexRetire(in, root, toolOutput, "post-boundary", toolOutput.RetireLabel)
			}
			if overInvestigateThreshold {
				return handleCodexRetire(in, root, toolOutput, investigateThresholdReason, toolOutput.RetireLabel)
			}
		}
		if toolOutput.Shell && pastBoundary && outputbudget.EstimatedTokens(toolOutput.Text) > outputbudget.TokenThreshold {
			return handleCodexRetire(in, root, toolOutput, "post-boundary", toolOutput.BudgetRetire)
		}
		budgeted, omittedLines, omittedBytes, ok, err := budgetCodexToolOutput(toolOutput, root, in.SessionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, nil
		}
		if err := recordStat(root, in.SessionID, func(s *stats.Session) {
			s.Agent = stats.AgentCodex
			s.RecordBudgetTrim(omittedLines, omittedBytes)
		}); err != nil {
			return nil, err
		}
		return codexReplacementOutput(budgeted), nil
	}
	if err := ledger.Save(root, in.SessionID, st); err != nil {
		return nil, err
	}
	if err := recordStat(root, in.SessionID, func(s *stats.Session) {
		s.Agent = stats.AgentCodex
		s.RecordDedup(len(toolOutput.Text))
	}); err != nil {
		return nil, err
	}

	return codexReplacementOutput(fmt.Sprintf("[agent-winglet] unchanged since turn %d (%s)", repeatOfTurn, toolOutput.Key)), nil
}

func observeCodexPhase(root, sessionID string, isInvestigate, isImplement bool) (bool, error) {
	if !isInvestigate && !isImplement {
		return false, nil
	}
	st, err := phase.Load(root, sessionID)
	if err != nil {
		return false, err
	}
	crossed := st.Observe(isInvestigate, isImplement)
	return crossed, phase.Save(root, sessionID, st)
}

func codexPhaseStatus(root, sessionID string) (pastBoundary bool, overInvestigateThreshold bool, err error) {
	st, err := phase.Load(root, sessionID)
	if err != nil {
		return false, false, err
	}
	return st.Suggested, st.InvestigateCalls > phase.InvestigateCallThreshold, nil
}

func codexPhaseClass(in hookInput) cmdclass.Class {
	if codexShellTool(in.ToolName) {
		command, ok := codexShellCommand(in.ToolInput)
		if !ok {
			return cmdclass.Neutral
		}
		return cmdclass.Classify(command)
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(in.ToolName)), "mcp__") {
		return cmdclass.Neutral
	}
	return codexLocalToolClass(in.ToolName)
}

func handleCodexRetire(in hookInput, root string, toolOutput codexToolOutput, reason, label string) (*hookOutput, error) {
	path, err := retire.Store(root, in.SessionID, []byte(toolOutput.Text))
	if err != nil {
		return nil, err
	}
	n := len(toolOutput.Text)
	receipt := fmt.Sprintf(
		"[agent-winglet] %s retired %s (%s, %d bytes) - full output at %s",
		label, reason, toolOutput.Key, n, path,
	)
	if err := recordStat(root, in.SessionID, func(s *stats.Session) {
		s.Agent = stats.AgentCodex
		s.RecordRetire(n)
	}); err != nil {
		return nil, err
	}
	return codexReplacementOutput(receipt), nil
}

func budgetCodexToolOutput(toolOutput codexToolOutput, root, sessionID string) (string, int, int64, bool, error) {
	if toolOutput.Shell {
		return outputbudget.Stdout(toolOutput.Text, root, sessionID)
	}
	return outputbudget.TextField(toolOutput.Text, root, sessionID)
}

func codexNormalizeToolOutput(in hookInput, class cmdclass.Class) (codexToolOutput, bool) {
	if in.HookEventName != "PostToolUse" {
		return codexToolOutput{}, false
	}
	if codexShellTool(in.ToolName) {
		command, ok := codexShellCommand(in.ToolInput)
		if !ok {
			return codexToolOutput{}, false
		}
		output, ok := codexModelVisibleOutput(in.ToolResponse)
		if !ok {
			return codexToolOutput{}, false
		}
		return codexToolOutput{
			Key:          "Bash:" + command,
			Text:         output,
			Shell:        true,
			CanRewrite:   true,
			RetireLabel:  "investigate output",
			BudgetRetire: "bash output",
		}, true
	}

	name := strings.TrimSpace(in.ToolName)
	if name == "" || strings.HasPrefix(strings.ToLower(name), "mcp__") {
		return codexToolOutput{}, false
	}
	if class != cmdclass.Investigate {
		return codexToolOutput{Key: name, CanRewrite: false}, true
	}
	output, ok := codexModelVisibleTextField(in.ToolResponse)
	if !ok {
		return codexToolOutput{}, false
	}
	return codexToolOutput{
		Key:         codexLocalToolKey(name, in.ToolInput),
		Text:        output,
		CanRewrite:  true,
		RetireLabel: "investigate output",
	}, true
}

func codexLocalToolKey(toolName string, rawInput json.RawMessage) string {
	toolName = strings.TrimSpace(toolName)
	input := strings.TrimSpace(string(rawInput))
	if input == "" {
		return toolName
	}
	var decoded interface{}
	if err := json.Unmarshal(rawInput, &decoded); err == nil {
		if compact, err := json.Marshal(decoded); err == nil {
			input = string(compact)
		}
	}
	return toolName + ":" + input
}

func codexLocalToolClass(toolName string) cmdclass.Class {
	base := codexToolBaseName(toolName)
	switch base {
	case "read_file", "read", "list_dir", "ls", "grep", "search", "find", "glob":
		return cmdclass.Investigate
	case "apply_patch", "edit", "write", "write_file", "replace":
		return cmdclass.Implement
	default:
		return cmdclass.Neutral
	}
}

func codexToolBaseName(toolName string) string {
	name := strings.ToLower(strings.TrimSpace(toolName))
	name = strings.TrimPrefix(name, "functions.")
	for _, sep := range []string{".", "/", ":"} {
		if i := strings.LastIndex(name, sep); i >= 0 {
			name = name[i+1:]
		}
	}
	return name
}

func codexShellTool(toolName string) bool {
	switch strings.ToLower(strings.TrimSpace(toolName)) {
	case "bash", "shell", "exec", "exec_command", "unified-exec", "unified_exec", "functions.exec_command":
		return true
	default:
		return false
	}
}

func codexShellCommand(raw json.RawMessage) (string, bool) {
	var input struct {
		Command string `json:"command"`
		Cmd     string `json:"cmd"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return "", false
	}
	command := strings.TrimSpace(input.Command)
	if command == "" {
		command = strings.TrimSpace(input.Cmd)
	}
	return command, command != ""
}

func codexModelVisibleOutput(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if rejectedModelVisibleText(text) {
			return "", false
		}
		return text, text != ""
	}

	var response codexToolResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", false
	}
	if !response.Successful() {
		return "", false
	}

	output := response.Text()
	return output, output != ""
}

func codexModelVisibleTextField(raw json.RawMessage) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if rejectedModelVisibleText(text) {
			return "", false
		}
		return text, text != ""
	}

	var response codexToolResponse
	if err := json.Unmarshal(raw, &response); err != nil {
		return "", false
	}
	if !response.Successful() {
		return "", false
	}

	output := response.TextField()
	return output, output != ""
}

type codexToolResponse struct {
	Stdout      string `json:"stdout"`
	Stderr      string `json:"stderr"`
	Output      string `json:"output"`
	Content     string `json:"content"`
	Error       string `json:"error"`
	Interrupted bool   `json:"interrupted"`
	IsImage     bool   `json:"is_image"`
	ExitCode    *int   `json:"exit_code"`
	ExitCodeAlt *int   `json:"exitCode"`
	Status      string `json:"status"`
	Success     *bool  `json:"success"`
	Result      *struct {
		Stdout      string `json:"stdout"`
		Stderr      string `json:"stderr"`
		Output      string `json:"output"`
		Content     string `json:"content"`
		Error       string `json:"error"`
		Interrupted bool   `json:"interrupted"`
		IsImage     bool   `json:"is_image"`
		ExitCode    *int   `json:"exit_code"`
		ExitCodeAlt *int   `json:"exitCode"`
		Status      string `json:"status"`
		Success     *bool  `json:"success"`
	} `json:"result"`
}

func (r codexToolResponse) Successful() bool {
	if r.Interrupted || r.IsImage || r.Stderr != "" || r.Error != "" {
		return false
	}
	if r.ExitCode != nil && *r.ExitCode != 0 {
		return false
	}
	if r.ExitCodeAlt != nil && *r.ExitCodeAlt != 0 {
		return false
	}
	if r.Success != nil && !*r.Success {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(r.Status)) {
	case "error", "failed", "failure", "interrupted", "cancelled", "canceled":
		return false
	}
	if r.Result != nil {
		nested := codexToolResponse{
			Stdout:      r.Result.Stdout,
			Stderr:      r.Result.Stderr,
			Output:      r.Result.Output,
			Content:     r.Result.Content,
			Error:       r.Result.Error,
			Interrupted: r.Result.Interrupted,
			IsImage:     r.Result.IsImage,
			ExitCode:    r.Result.ExitCode,
			ExitCodeAlt: r.Result.ExitCodeAlt,
			Status:      r.Result.Status,
			Success:     r.Result.Success,
		}
		return nested.Successful()
	}
	switch strings.ToLower(strings.TrimSpace(r.Status)) {
	case "", "ok", "success", "succeeded", "completed":
		return true
	default:
		return true
	}
}

func (r codexToolResponse) Text() string {
	if r.Result != nil {
		nested := codexToolResponse{
			Stdout:  r.Result.Stdout,
			Output:  r.Result.Output,
			Content: r.Result.Content,
		}
		return nested.Text()
	}
	switch {
	case r.Stdout != "":
		return r.Stdout
	case r.Output != "":
		return r.Output
	default:
		return r.Content
	}
}

func (r codexToolResponse) TextField() string {
	if r.Result != nil {
		nested := codexToolResponse{
			Output:  r.Result.Output,
			Content: r.Result.Content,
		}
		return nested.TextField()
	}
	if r.Output != "" {
		return r.Output
	}
	return r.Content
}

func rejectedModelVisibleText(text string) bool {
	lower := strings.ToLower(text)
	idx := strings.Index(lower, "exit code:")
	if idx < 0 {
		return false
	}
	rest := strings.TrimSpace(lower[idx+len("exit code:"):])
	return rest == "" || rest[0] < '0' || rest[0] > '9' || rest[0] != '0'
}

func codexReplacementOutput(receipt string) *hookOutput {
	keepGoing := false
	return &hookOutput{
		Continue:      &keepGoing,
		StopReason:    receipt,
		SystemMessage: receipt,
	}
}

func codexCompactNudgeOutput() *hookOutput {
	if cfg, err := config.Load(); err == nil && cfg.CompactNudgeDisabled {
		return nil
	}

	const msg = "[agent-winglet] /compact nudge - you can compact the " +
		"session ahead of implementation, to save context while what's " +
		"still relevant is still clear."
	const modelInstruction = msg + " Before continuing with any further " +
		"work, tell the user they can run /compact now if they want to compact first."

	return &hookOutput{
		SystemMessage: msg,
		HookSpecificOutput: &hookSpecificOutput{
			HookEventName:     "PostToolUse",
			AdditionalContext: modelInstruction,
		},
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
	// Note: stats' per-session tally is deliberately left untouched here —
	// see the matching comment in cmd/claude-hook's handle. Dedup/budget-
	// trim/retire savings already happened and a compact or resume doesn't
	// undo them; only ledger/phase/retire's own detection state resets.
	if err := ensureCodexSessionVisible(root, in.SessionID); err != nil {
		return err
	}
	return registry.Register(root)
}

func ensureCodexSessionVisible(projectDir, sessionID string) error {
	s, err := stats.LoadSession(projectDir, sessionID)
	if err != nil {
		return err
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

func recordStat(projectDir, sessionID string, mutate func(*stats.Session)) error {
	s, err := stats.LoadSession(projectDir, sessionID)
	if err != nil {
		return err
	}
	mutate(s)
	return stats.SaveSession(projectDir, sessionID, s)
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
