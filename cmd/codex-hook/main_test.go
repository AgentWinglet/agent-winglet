package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umitkaanusta/agent-winglet/internal/config"
	"github.com/umitkaanusta/agent-winglet/internal/outputbudget"
	"github.com/umitkaanusta/agent-winglet/internal/phase"
	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/stats"
)

func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "agent-winglet-codex-hook-test-home")
	if err != nil {
		fmt.Fprintln(os.Stderr, "TestMain: MkdirTemp failed:", err)
		os.Exit(1)
	}
	os.Setenv("HOME", home)
	os.Setenv("AGENT_WINGLET_TEST_ALLOW_UNGATED_HOOKS", "1")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

func TestEntitlementGateTellsCodexWhenSignedOut(t *testing.T) {
	t.Setenv("AGENT_WINGLET_TEST_ALLOW_UNGATED_HOOKS", "0")
	t.Setenv("HOME", t.TempDir())

	out, err := handle(hookInput{
		SessionID:     "session-1",
		Cwd:           t.TempDir(),
		HookEventName: "SessionStart",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "not signed in") {
		t.Fatalf("SystemMessage = %+v, want signed-in notice", out)
	}
	if out.HookSpecificOutput == nil || out.HookSpecificOutput.AdditionalContext == "" {
		t.Fatalf("AdditionalContext should tell Codex the hook is gated")
	}
	if !strings.Contains(out.HookSpecificOutput.AdditionalContext, "ask the user") {
		t.Fatalf("AdditionalContext = %q, want an instruction to ask the user directly", out.HookSpecificOutput.AdditionalContext)
	}
	if strings.Contains(out.HookSpecificOutput.AdditionalContext, "AskUserQuestion") {
		t.Fatalf("AdditionalContext should not mention Claude-specific AskUserQuestion: %q", out.HookSpecificOutput.AdditionalContext)
	}
}

// TestEntitlementGateStaysBlockedAfterFirstNoticeInSameSession guards against
// a bug where the gate only actually blocked the first hook call of a
// session: entitlement.ShouldEmitNotice throttles the *visible* notice to
// once per session, but handle() used to treat "no notice to show" and
// "allowed" identically, so every call after the first quietly ran the full
// suppression logic — including recording state that later calls could then
// dedup-match against.
func TestEntitlementGateStaysBlockedAfterFirstNoticeInSameSession(t *testing.T) {
	t.Setenv("AGENT_WINGLET_TEST_ALLOW_UNGATED_HOOKS", "0")
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-gate-session"

	if _, err := handle(hookInput{SessionID: sessionID, Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatal(err)
	}

	in := codexBashPostInput(t, dir, sessionID, "echo hi", map[string]interface{}{
		"stdout":    "hi\n",
		"stderr":    "",
		"exit_code": 0,
	})

	first, err := handle(in)
	if err != nil {
		t.Fatalf("first Bash call errored: %v", err)
	}
	if strings.Contains(first.SystemMessage, "unchanged since turn") {
		t.Fatalf("blocked session should never produce a dedup substitution, got %+v", first)
	}
	if first.SystemMessage != "" || first.HookSpecificOutput != nil {
		t.Fatalf("blocked call after the session's first notice shouldn't repeat it or the nudge, got %+v", first)
	}

	second, err := handle(in)
	if err != nil {
		t.Fatalf("second Bash call errored: %v", err)
	}
	if strings.Contains(second.SystemMessage, "unchanged since turn") {
		t.Fatalf("still-blocked session substituted a repeat call — the gate stopped applying after the first blocked call: %+v", second)
	}
}

func writeRollout(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	var data []byte
	for _, line := range lines {
		data = append(data, []byte(line)...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}
	return path
}

func appendRollout(t *testing.T, path string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile errored: %v", err)
	}
	defer f.Close()
	for _, line := range lines {
		if _, err := f.WriteString(line + "\n"); err != nil {
			t.Fatalf("WriteString errored: %v", err)
		}
	}
}

func linesOfApproxTokens(tokens int) string {
	lines := make([]string, 2*tokens)
	for i := range lines {
		lines[i] = "x"
	}
	return strings.Join(lines, "\n") + "\n"
}

func archivePathFromBudgetedOutput(t *testing.T, body, marker string) string {
	t.Helper()
	idx := strings.Index(body, marker)
	if idx == -1 {
		t.Fatalf("body missing archive marker %q: %q", marker, body)
	}
	path := body[idx+len(marker):]
	if nl := strings.IndexByte(path, '\n'); nl != -1 {
		path = path[:nl]
	}
	return path
}

func TestSessionStartRegistersProjectAndPreservesSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"

	s := &stats.Session{Agent: stats.AgentCodex}
	s.RecordDedup(10)
	if err := stats.SaveSession(dir, sessionID, s); err != nil {
		t.Fatalf("SaveSession errored: %v", err)
	}

	out, err := handle(hookInput{SessionID: sessionID, Cwd: dir, HookEventName: "SessionStart"})
	if err != nil {
		t.Fatalf("handle SessionStart errored: %v", err)
	}
	if out != nil {
		t.Fatalf("SessionStart should not emit output, got %+v", out)
	}

	dirs, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load errored: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != dir {
		t.Fatalf("registered dirs = %v, want [%s]", dirs, dir)
	}

	// A resumed session_id's prior savings already happened and a
	// SessionStart doesn't undo them — only ledger/phase/retire's own
	// detection state resets, not this receipt.
	got, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if got.DedupHits != 1 || got.DedupBytes != 10 {
		t.Fatalf("SessionStart wiped prior savings: %+v", got)
	}
	if got.Agent != stats.AgentCodex {
		t.Fatalf("Agent = %q, want %q", got.Agent, stats.AgentCodex)
	}
}

func TestSessionStartCreatesVisibleCodexSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "fresh-codex-session"

	if _, err := handle(hookInput{SessionID: sessionID, Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("handle SessionStart errored: %v", err)
	}

	files, err := stats.ListSessions(dir)
	if err != nil {
		t.Fatalf("ListSessions errored: %v", err)
	}
	if len(files) != 1 || files[0].ID != sessionID {
		t.Fatalf("session files = %+v, want one visible Codex session %q", files, sessionID)
	}

	got, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if got.Agent != stats.AgentCodex {
		t.Fatalf("Agent = %q, want %q", got.Agent, stats.AgentCodex)
	}
	if !got.IsZero() || got.TranscriptContentBytes != 0 {
		t.Fatalf("fresh session should only be a visibility marker, got %+v", got)
	}
}

func TestPostCompactPreservesCodexAgentAndSavings(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"

	s := &stats.Session{
		TranscriptTokens:       100,
		TranscriptContentBytes: 50,
		TranscriptOffset:       500,
	}
	s.RecordDedup(10)
	if err := stats.SaveSession(dir, sessionID, s); err != nil {
		t.Fatalf("SaveSession errored: %v", err)
	}

	if _, err := handle(hookInput{SessionID: sessionID, Cwd: dir, HookEventName: "PostCompact"}); err != nil {
		t.Fatalf("PostCompact errored: %v", err)
	}

	got, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if got.Agent != stats.AgentCodex {
		t.Fatalf("Agent = %q, want %q", got.Agent, stats.AgentCodex)
	}
	// A compact doesn't undo bytes that were already kept out of the
	// model's context — the mechanism counters must survive it, same as
	// transcript usage.
	if got.DedupHits != 1 || got.DedupBytes != 10 {
		t.Fatalf("mechanism counters should survive PostCompact: %+v", got)
	}
	if got.TranscriptTokens != 100 || got.TranscriptContentBytes != 50 || got.TranscriptOffset != 500 {
		t.Fatalf("transcript usage should be preserved after PostCompact: %+v", got)
	}
}

func TestPostToolUseRecordsCodexTaggedUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	rollout := writeRollout(t, []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"hello","model":"gpt-5-codex"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":80,"cache_write_input_tokens":10,"output_tokens":5,"reasoning_output_tokens":2,"total_tokens":115}}}}`,
	})

	out, err := handle(hookInput{
		SessionID:      sessionID,
		Cwd:            dir,
		HookEventName:  "PostToolUse",
		TranscriptPath: rollout,
	})
	if err != nil {
		t.Fatalf("handle PostToolUse errored: %v", err)
	}
	if out != nil {
		t.Fatalf("stats-only PostToolUse should not emit output, got %+v", out)
	}

	s, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.Agent != stats.AgentCodex {
		t.Fatalf("Agent = %q, want %q", s.Agent, stats.AgentCodex)
	}
	// input_tokens=100, cached_input_tokens=80 (a subset of input_tokens,
	// not additional), cache_write_input_tokens=10 -> fresh input = 20,
	// TranscriptTokens = 20+10 = 30.
	if s.TranscriptTokens != 30 {
		t.Fatalf("TranscriptTokens = %d, want 30", s.TranscriptTokens)
	}
	if s.TranscriptContentBytes != int64(len("hello")) {
		t.Fatalf("TranscriptContentBytes = %d, want %d", s.TranscriptContentBytes, len("hello"))
	}
	if s.TranscriptOffset == 0 {
		t.Fatalf("TranscriptOffset should advance")
	}
}

func TestPostToolUseDedupsRepeatedBashOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	first := codexBashPostInput(t, dir, sessionID, "echo hi", map[string]interface{}{
		"stdout":    "hi\n",
		"stderr":    "",
		"exit_code": 0,
	})

	out, err := handle(first)
	if err != nil {
		t.Fatalf("first PostToolUse errored: %v", err)
	}
	if out != nil {
		t.Fatalf("first observation should not emit output, got %+v", out)
	}

	out, err = handle(codexBashPostInput(t, dir, sessionID, "echo hi", map[string]interface{}{
		"stdout":    "hi\n",
		"stderr":    "",
		"exit_code": 0,
	}))
	if err != nil {
		t.Fatalf("repeat PostToolUse errored: %v", err)
	}
	if out == nil {
		t.Fatalf("expected dedup replacement output")
	}
	if out.Continue == nil || *out.Continue {
		t.Fatalf("Continue = %v, want pointer to false", out.Continue)
	}
	if !strings.Contains(out.SystemMessage, "unchanged since turn 1 (Bash:echo hi)") {
		t.Fatalf("SystemMessage = %q, want dedup receipt", out.SystemMessage)
	}
	if out.StopReason != out.SystemMessage {
		t.Fatalf("StopReason = %q, want SystemMessage %q", out.StopReason, out.SystemMessage)
	}
	if out.Decision != "" || out.HookSpecificOutput != nil {
		t.Fatalf("dedup should use continue:false replacement shape, got %+v", out)
	}

	s, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.Agent != stats.AgentCodex {
		t.Fatalf("Agent = %q, want %q", s.Agent, stats.AgentCodex)
	}
	if s.DedupHits != 1 || s.DedupBytes != int64(len("hi\n")) {
		t.Fatalf("DedupHits/DedupBytes = %d/%d, want 1/%d", s.DedupHits, s.DedupBytes, len("hi\n"))
	}
}

func TestPostToolUseBudgetsLongFirstTimeBashOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	longOutput := linesOfApproxTokens(outputbudget.TokenThreshold + 1)

	out, err := handle(codexBashPostInput(t, dir, sessionID, "go test ./...", map[string]interface{}{
		"stdout":    longOutput,
		"stderr":    "",
		"exit_code": 0,
	}))
	if err != nil {
		t.Fatalf("PostToolUse errored: %v", err)
	}
	if out == nil {
		t.Fatalf("expected budget replacement output")
	}
	if out.Continue == nil || *out.Continue {
		t.Fatalf("Continue = %v, want pointer to false", out.Continue)
	}
	if out.SystemMessage == longOutput {
		t.Fatalf("budgeting did not replace original output")
	}
	if !strings.Contains(out.SystemMessage, "lines omitted") {
		t.Fatalf("budgeted output missing omission marker: %q", out.SystemMessage)
	}
	if !strings.Contains(out.SystemMessage, "full output at ") {
		t.Fatalf("budgeted output missing archive path: %q", out.SystemMessage)
	}
	if out.StopReason != out.SystemMessage {
		t.Fatalf("StopReason = %q, want SystemMessage %q", out.StopReason, out.SystemMessage)
	}

	path := archivePathFromBudgetedOutput(t, out.SystemMessage, "full output at ")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading archived output at %q errored: %v", path, err)
	}
	if string(stored) != longOutput {
		t.Fatalf("archived output = %q, want original output", stored)
	}

	s, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.Agent != stats.AgentCodex {
		t.Fatalf("Agent = %q, want %q", s.Agent, stats.AgentCodex)
	}
	if s.BudgetTrims != 1 {
		t.Fatalf("BudgetTrims = %d, want 1", s.BudgetTrims)
	}
	if s.BudgetLinesOmitted <= 0 || s.BudgetBytesOmitted <= 0 {
		t.Fatalf("BudgetLinesOmitted/BudgetBytesOmitted = %d/%d, want positive", s.BudgetLinesOmitted, s.BudgetBytesOmitted)
	}
}

func TestPostToolUseBudgetsUnifiedExecCmdOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	longOutput := linesOfApproxTokens(outputbudget.TokenThreshold + 1)

	out, err := handle(codexShellPostInput(t, dir, sessionID, "unified-exec", "cmd", "rg --files", map[string]interface{}{
		"output":   longOutput,
		"status":   "completed",
		"success":  true,
		"exitCode": 0,
	}))
	if err != nil {
		t.Fatalf("PostToolUse errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "lines omitted") {
		t.Fatalf("unified-exec long output should be budgeted, got %+v", out)
	}
}

func TestPostToolUseDedupTakesPrecedenceOverBudgeting(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	longOutput := linesOfApproxTokens(outputbudget.TokenThreshold + 1)
	input := codexBashPostInput(t, dir, sessionID, "go test ./...", map[string]interface{}{
		"stdout":    longOutput,
		"stderr":    "",
		"exit_code": 0,
	})

	first, err := handle(input)
	if err != nil {
		t.Fatalf("first PostToolUse errored: %v", err)
	}
	if first == nil || !strings.Contains(first.SystemMessage, "lines omitted") {
		t.Fatalf("first long output should be budgeted, got %+v", first)
	}
	second, err := handle(input)
	if err != nil {
		t.Fatalf("repeat PostToolUse errored: %v", err)
	}
	if second == nil || !strings.Contains(second.SystemMessage, "unchanged since turn 1 (Bash:go test ./...)") {
		t.Fatalf("repeat long output should be deduped, got %+v", second)
	}
	if strings.Contains(second.SystemMessage, "lines omitted") {
		t.Fatalf("repeat should dedup instead of budget, got %q", second.SystemMessage)
	}
}

func TestPostToolUseRetiresInvestigateShellAfterBoundaryCrossed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"

	if out, err := handle(codexBashPostInput(t, dir, sessionID, "rg --files", map[string]interface{}{
		"stdout":    "SPEC.md\n",
		"stderr":    "",
		"exit_code": 0,
	})); err != nil {
		t.Fatalf("investigate seed errored: %v", err)
	} else if out != nil {
		t.Fatalf("pre-boundary investigate shell should pass through, got %+v", out)
	}
	if out, err := handle(codexApplyPatchInput(t, dir, sessionID)); err != nil {
		t.Fatalf("apply_patch boundary signal errored: %v", err)
	} else if out == nil || !strings.Contains(out.SystemMessage, "/compact nudge") {
		t.Fatalf("apply_patch should emit the Phase 9 boundary nudge, got %+v", out)
	}

	output := "package main\n"
	out, err := handle(codexBashPostInput(t, dir, sessionID, "cat main.go", map[string]interface{}{
		"stdout":    output,
		"stderr":    "",
		"exit_code": 0,
	}))
	if err != nil {
		t.Fatalf("post-boundary investigate shell errored: %v", err)
	}
	if out == nil {
		t.Fatalf("expected post-boundary investigate shell retirement")
	}
	if !strings.Contains(out.SystemMessage, "investigate output retired post-boundary") {
		t.Fatalf("SystemMessage = %q, want post-boundary retire receipt", out.SystemMessage)
	}
	path := archivePathFromBudgetedOutput(t, out.SystemMessage, "full output at ")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading retired output at %q errored: %v", path, err)
	}
	if string(stored) != output {
		t.Fatalf("retired output = %q, want %q", stored, output)
	}

	s, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.Agent != stats.AgentCodex || s.RetiredCalls != 1 || s.RetiredBytes != int64(len(output)) {
		t.Fatalf("stats after retire = %+v, want codex agent and one retire", s)
	}
}

func TestPostToolUseRetiresInvestigateShellAfterThresholdExceededPreBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"

	for i := 0; i < phase.InvestigateCallThreshold; i++ {
		out, err := handle(codexBashPostInput(t, dir, sessionID, fmt.Sprintf("ls dir-%d", i), map[string]interface{}{
			"stdout":    fmt.Sprintf("file-%d\n", i),
			"stderr":    "",
			"exit_code": 0,
		}))
		if err != nil {
			t.Fatalf("seed call %d errored: %v", i+1, err)
		}
		if out != nil {
			t.Fatalf("seed call %d should pass through, got %+v", i+1, out)
		}
	}

	output := "one more file\n"
	out, err := handle(codexBashPostInput(t, dir, sessionID, "find .", map[string]interface{}{
		"stdout":    output,
		"stderr":    "",
		"exit_code": 0,
	}))
	if err != nil {
		t.Fatalf("threshold call errored: %v", err)
	}
	if out == nil {
		t.Fatalf("expected threshold retirement")
	}
	if !strings.Contains(out.SystemMessage, investigateThresholdReason) {
		t.Fatalf("SystemMessage = %q, want threshold reason", out.SystemMessage)
	}
	if strings.Contains(out.SystemMessage, "post-boundary") {
		t.Fatalf("threshold receipt should not claim post-boundary: %q", out.SystemMessage)
	}
}

func TestPostToolUseRetiresLongNeutralShellOutputPostBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"

	if _, err := handle(codexBashPostInput(t, dir, sessionID, "rg --files", map[string]interface{}{
		"stdout":    "SPEC.md\n",
		"stderr":    "",
		"exit_code": 0,
	})); err != nil {
		t.Fatalf("investigate seed errored: %v", err)
	}
	if _, err := handle(codexApplyPatchInput(t, dir, sessionID)); err != nil {
		t.Fatalf("apply_patch boundary signal errored: %v", err)
	}

	longOutput := linesOfApproxTokens(outputbudget.TokenThreshold + 1)
	out, err := handle(codexBashPostInput(t, dir, sessionID, "go test ./...", map[string]interface{}{
		"stdout":    longOutput,
		"stderr":    "",
		"exit_code": 0,
	}))
	if err != nil {
		t.Fatalf("post-boundary neutral shell errored: %v", err)
	}
	if out == nil {
		t.Fatalf("expected long neutral shell retirement")
	}
	if !strings.Contains(out.SystemMessage, "bash output retired post-boundary") {
		t.Fatalf("SystemMessage = %q, want bash retire receipt", out.SystemMessage)
	}
	if strings.Contains(out.SystemMessage, "lines omitted") {
		t.Fatalf("post-boundary long output should retire instead of budget: %q", out.SystemMessage)
	}
	path := archivePathFromBudgetedOutput(t, out.SystemMessage, "full output at ")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading retired output at %q errored: %v", path, err)
	}
	if string(stored) != longOutput {
		t.Fatalf("retired output = %q, want original long output", stored)
	}
}

func TestSubagentEventsCountAsInvestigationForCodexBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"

	if out, err := handle(hookInput{SessionID: sessionID, Cwd: dir, HookEventName: "SubagentStart"}); err != nil {
		t.Fatalf("SubagentStart errored: %v", err)
	} else if out != nil {
		t.Fatalf("SubagentStart should not emit output, got %+v", out)
	}
	if _, err := handle(codexApplyPatchInput(t, dir, sessionID)); err != nil {
		t.Fatalf("apply_patch boundary signal errored: %v", err)
	}

	out, err := handle(codexBashPostInput(t, dir, sessionID, "cat README.md", map[string]interface{}{
		"stdout":    "readme\n",
		"stderr":    "",
		"exit_code": 0,
	}))
	if err != nil {
		t.Fatalf("post-subagent boundary shell errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "investigate output retired post-boundary") {
		t.Fatalf("subagent should count as investigation before apply_patch boundary, got %+v", out)
	}
}

func TestPostToolUseDedupTakesPrecedenceOverRetirementPostBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	longOutput := linesOfApproxTokens(outputbudget.TokenThreshold + 1)
	input := codexBashPostInput(t, dir, sessionID, "go test ./...", map[string]interface{}{
		"stdout":    longOutput,
		"stderr":    "",
		"exit_code": 0,
	})

	if first, err := handle(input); err != nil {
		t.Fatalf("first PostToolUse errored: %v", err)
	} else if first == nil || !strings.Contains(first.SystemMessage, "lines omitted") {
		t.Fatalf("first long output should be budgeted, got %+v", first)
	}
	if _, err := handle(codexBashPostInput(t, dir, sessionID, "rg --files", map[string]interface{}{
		"stdout":    "SPEC.md\n",
		"stderr":    "",
		"exit_code": 0,
	})); err != nil {
		t.Fatalf("investigate seed errored: %v", err)
	}
	if _, err := handle(codexApplyPatchInput(t, dir, sessionID)); err != nil {
		t.Fatalf("apply_patch boundary signal errored: %v", err)
	}

	out, err := handle(input)
	if err != nil {
		t.Fatalf("repeat PostToolUse errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "unchanged since turn 1 (Bash:go test ./...)") {
		t.Fatalf("repeat post-boundary output should be deduped, got %+v", out)
	}
	if strings.Contains(out.SystemMessage, "retired post-boundary") {
		t.Fatalf("repeat should dedup instead of retire, got %q", out.SystemMessage)
	}
}

func TestPostToolUseEmitsCodexCompactNudgeOnceOnBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"

	if out, err := handle(codexBashPostInput(t, dir, sessionID, "rg --files", map[string]interface{}{
		"stdout":    "SPEC.md\n",
		"stderr":    "",
		"exit_code": 0,
	})); err != nil {
		t.Fatalf("investigate seed errored: %v", err)
	} else if out != nil {
		t.Fatalf("pre-boundary investigate shell should pass through, got %+v", out)
	}

	out, err := handle(codexApplyPatchInput(t, dir, sessionID))
	if err != nil {
		t.Fatalf("apply_patch boundary signal errored: %v", err)
	}
	if out == nil {
		t.Fatalf("expected compact nudge on first boundary crossing")
	}
	if out.Continue != nil || out.StopReason != "" {
		t.Fatalf("compact nudge should not replace or stop the tool result, got %+v", out)
	}
	if !strings.Contains(out.SystemMessage, "/compact nudge") {
		t.Fatalf("SystemMessage = %q, want /compact nudge", out.SystemMessage)
	}
	if out.HookSpecificOutput == nil || out.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Fatalf("HookSpecificOutput = %+v, want PostToolUse additional context", out.HookSpecificOutput)
	}
	if !strings.Contains(out.HookSpecificOutput.AdditionalContext, "tell the user") {
		t.Fatalf("AdditionalContext = %q, want Codex-native user-facing instruction", out.HookSpecificOutput.AdditionalContext)
	}
	if strings.Contains(out.HookSpecificOutput.AdditionalContext, "AskUserQuestion") {
		t.Fatalf("AdditionalContext should not mention Claude-specific AskUserQuestion: %q", out.HookSpecificOutput.AdditionalContext)
	}

	out, err = handle(codexApplyPatchInput(t, dir, sessionID))
	if err != nil {
		t.Fatalf("second apply_patch errored: %v", err)
	}
	if out != nil {
		t.Fatalf("compact nudge should fire once per phase state, got %+v", out)
	}
}

func TestPostToolUseSkipsCodexCompactNudgeWhenDisabled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	if err := config.Save(&config.Config{CompactNudgeDisabled: true}); err != nil {
		t.Fatalf("config.Save errored: %v", err)
	}

	if _, err := handle(codexBashPostInput(t, dir, sessionID, "rg --files", map[string]interface{}{
		"stdout":    "SPEC.md\n",
		"stderr":    "",
		"exit_code": 0,
	})); err != nil {
		t.Fatalf("investigate seed errored: %v", err)
	}

	out, err := handle(codexApplyPatchInput(t, dir, sessionID))
	if err != nil {
		t.Fatalf("apply_patch boundary signal errored: %v", err)
	}
	if out != nil {
		t.Fatalf("disabled compact nudge should emit nothing, got %+v", out)
	}
}

func TestPostToolUseDedupUsesModelFacingStringOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	response := "Command: rg --files\nOutput:\nSPEC.md\n"

	if out, err := handle(codexBashPostInput(t, dir, sessionID, "rg --files", response)); err != nil {
		t.Fatalf("first PostToolUse errored: %v", err)
	} else if out != nil {
		t.Fatalf("first observation should not emit output, got %+v", out)
	}
	out, err := handle(codexBashPostInput(t, dir, sessionID, "rg --files", response))
	if err != nil {
		t.Fatalf("repeat PostToolUse errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "Bash:rg --files") {
		t.Fatalf("dedup output = %+v, want receipt for model-facing string output", out)
	}
}

func TestPostToolUseDedupIgnoresFailedInterruptedAndImageBashOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	cases := []struct {
		name     string
		response interface{}
	}{
		{name: "stderr", response: map[string]interface{}{"stdout": "bad\n", "stderr": "failed\n", "exit_code": 0}},
		{name: "exit_code", response: map[string]interface{}{"stdout": "bad\n", "stderr": "", "exit_code": 1}},
		{name: "exitCode", response: map[string]interface{}{"stdout": "bad\n", "stderr": "", "exitCode": 1}},
		{name: "success_false", response: map[string]interface{}{"stdout": "bad\n", "success": false}},
		{name: "status_failed", response: map[string]interface{}{"stdout": "bad\n", "status": "failed"}},
		{name: "interrupted", response: map[string]interface{}{"stdout": "bad\n", "interrupted": true}},
		{name: "image", response: map[string]interface{}{"stdout": "bad\n", "is_image": true}},
		{name: "string_nonzero_exit", response: "Command failed\nExit code: 42\nOutput:\nbad\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			command := "cmd-" + tc.name
			for i := 0; i < 2; i++ {
				out, err := handle(codexBashPostInput(t, dir, sessionID, command, tc.response))
				if err != nil {
					t.Fatalf("PostToolUse errored: %v", err)
				}
				if out != nil {
					t.Fatalf("failed output should not be deduped, got %+v", out)
				}
			}
		})
	}
}

func TestPostToolUseDedupReadsNestedResultOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	response := map[string]interface{}{
		"result": map[string]interface{}{
			"stdout":   "nested\n",
			"stderr":   "",
			"exitCode": 0,
		},
	}

	if out, err := handle(codexBashPostInput(t, dir, sessionID, "nested", response)); err != nil {
		t.Fatalf("first PostToolUse errored: %v", err)
	} else if out != nil {
		t.Fatalf("first observation should not emit output, got %+v", out)
	}
	out, err := handle(codexBashPostInput(t, dir, sessionID, "nested", response))
	if err != nil {
		t.Fatalf("repeat PostToolUse errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "Bash:nested") {
		t.Fatalf("dedup output = %+v, want receipt for nested output", out)
	}
}

func TestCodexModelVisibleTextField(t *testing.T) {
	successFalse := false
	exitCodeOne := 1
	cases := []struct {
		name string
		raw  interface{}
		want string
		ok   bool
	}{
		{name: "raw_string", raw: "plain text\n", want: "plain text\n", ok: true},
		{name: "output", raw: map[string]interface{}{"output": "from output"}, want: "from output", ok: true},
		{name: "content", raw: map[string]interface{}{"content": "from content"}, want: "from content", ok: true},
		{name: "result_output", raw: map[string]interface{}{"result": map[string]interface{}{"output": "nested output"}}, want: "nested output", ok: true},
		{name: "result_content", raw: map[string]interface{}{"result": map[string]interface{}{"content": "nested content"}}, want: "nested content", ok: true},
		{name: "stdout_is_shell_only", raw: map[string]interface{}{"stdout": "shell stdout"}, ok: false},
		{name: "error", raw: map[string]interface{}{"output": "bad", "error": "failed"}, ok: false},
		{name: "root_error_with_result", raw: map[string]interface{}{"error": "failed", "result": map[string]interface{}{"output": "bad"}}, ok: false},
		{name: "stderr", raw: map[string]interface{}{"output": "bad", "stderr": "failed"}, ok: false},
		{name: "interrupted", raw: map[string]interface{}{"output": "bad", "interrupted": true}, ok: false},
		{name: "image", raw: map[string]interface{}{"output": "bad", "is_image": true}, ok: false},
		{name: "success_false", raw: map[string]interface{}{"output": "bad", "success": successFalse}, ok: false},
		{name: "exit_code", raw: map[string]interface{}{"output": "bad", "exit_code": exitCodeOne}, ok: false},
		{name: "unknown_object", raw: map[string]interface{}{"value": "leave alone"}, ok: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.raw)
			if err != nil {
				t.Fatalf("Marshal errored: %v", err)
			}
			got, ok := codexModelVisibleTextField(raw)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("codexModelVisibleTextField() = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
		})
	}
}

func TestPostToolUseDedupsNonShellLocalReadTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	response := map[string]interface{}{"content": "same file\n"}

	if out, err := handle(codexLocalPostInput(t, dir, sessionID, "read_file", map[string]interface{}{"path": "main.go"}, response)); err != nil {
		t.Fatalf("first PostToolUse errored: %v", err)
	} else if out != nil {
		t.Fatalf("first local read should pass through, got %+v", out)
	}

	out, err := handle(codexLocalPostInput(t, dir, sessionID, "read_file", map[string]interface{}{"path": "main.go"}, response))
	if err != nil {
		t.Fatalf("repeat PostToolUse errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, `read_file:{"path":"main.go"}`) {
		t.Fatalf("repeat local read should dedup with tool/input key, got %+v", out)
	}

	s, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.Agent != stats.AgentCodex || s.DedupHits != 1 || s.DedupBytes != int64(len("same file\n")) {
		t.Fatalf("stats after non-shell dedup = %+v, want codex agent and one dedup", s)
	}
}

func TestPostToolUseBudgetsLongNonShellLocalReadTool(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	longOutput := linesOfApproxTokens(outputbudget.TokenThreshold + 1)

	out, err := handle(codexLocalPostInput(t, dir, sessionID, "grep", map[string]interface{}{"pattern": "TODO"}, map[string]interface{}{
		"output": longOutput,
	}))
	if err != nil {
		t.Fatalf("PostToolUse errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "lines omitted") || strings.Contains(out.SystemMessage, "exit 0") {
		t.Fatalf("long non-shell output should be budgeted without shell exit marker, got %+v", out)
	}
	path := archivePathFromBudgetedOutput(t, out.SystemMessage, "full output at ")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading archived output at %q errored: %v", path, err)
	}
	if string(stored) != longOutput {
		t.Fatalf("archived output = %q, want original output", stored)
	}
}

func TestPostToolUseRetiresNonShellLocalReadToolAfterBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"

	if out, err := handle(codexLocalPostInput(t, dir, sessionID, "list_dir", map[string]interface{}{"path": "."}, map[string]interface{}{
		"output": "main.go\n",
	})); err != nil {
		t.Fatalf("investigate seed errored: %v", err)
	} else if out != nil {
		t.Fatalf("pre-boundary local read should pass through, got %+v", out)
	}
	if out, err := handle(codexApplyPatchInput(t, dir, sessionID)); err != nil {
		t.Fatalf("apply_patch boundary signal errored: %v", err)
	} else if out == nil || !strings.Contains(out.SystemMessage, "/compact nudge") {
		t.Fatalf("apply_patch should emit the boundary nudge, got %+v", out)
	}

	output := "package main\n"
	out, err := handle(codexLocalPostInput(t, dir, sessionID, "read_file", map[string]interface{}{"path": "main.go"}, map[string]interface{}{
		"content": output,
	}))
	if err != nil {
		t.Fatalf("post-boundary local read errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "investigate output retired post-boundary") {
		t.Fatalf("post-boundary local read should retire, got %+v", out)
	}
	path := archivePathFromBudgetedOutput(t, out.SystemMessage, "full output at ")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading retired output at %q errored: %v", path, err)
	}
	if string(stored) != output {
		t.Fatalf("retired output = %q, want %q", stored, output)
	}
}

func TestPostToolUseRetiresNonShellLocalReadToolAfterThresholdExceededPreBoundary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"

	for i := 0; i < phase.InvestigateCallThreshold; i++ {
		out, err := handle(codexLocalPostInput(t, dir, sessionID, "read_file", map[string]interface{}{"path": fmt.Sprintf("file-%d.go", i)}, map[string]interface{}{
			"content": fmt.Sprintf("package p%d\n", i),
		}))
		if err != nil {
			t.Fatalf("seed call %d errored: %v", i+1, err)
		}
		if out != nil {
			t.Fatalf("seed call %d should pass through, got %+v", i+1, out)
		}
	}

	output := "one more file\n"
	out, err := handle(codexLocalPostInput(t, dir, sessionID, "read_file", map[string]interface{}{"path": "tail.go"}, map[string]interface{}{
		"content": output,
	}))
	if err != nil {
		t.Fatalf("threshold call errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, investigateThresholdReason) {
		t.Fatalf("threshold local read should retire, got %+v", out)
	}
	path := archivePathFromBudgetedOutput(t, out.SystemMessage, "full output at ")
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading retired output at %q errored: %v", path, err)
	}
	if string(stored) != output {
		t.Fatalf("retired output = %q, want %q", stored, output)
	}
}

func TestPostToolUseLeavesUnknownStructuredNonShellResponseUntouched(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	response := map[string]interface{}{"items": []string{"one", "two"}}

	for i := 0; i < 2; i++ {
		out, err := handle(codexLocalPostInput(t, dir, sessionID, "read_file", map[string]interface{}{"path": "main.go"}, response))
		if err != nil {
			t.Fatalf("PostToolUse %d errored: %v", i+1, err)
		}
		if out != nil {
			t.Fatalf("unknown structured response should pass through untouched, got %+v", out)
		}
	}
}

func TestPostToolUseProbeDisabledByDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(codexProbeEnvVar, "")

	out, err := handle(codexProbeInput(t, t.TempDir(), "printf 'agent-winglet-codex-probe\n'"))
	if err != nil {
		t.Fatalf("handle PostToolUse errored: %v", err)
	}
	if out != nil {
		t.Fatalf("probe should be disabled by default, got %+v", out)
	}
}

func TestPostToolUseProbeContinueFalseOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(codexProbeEnvVar, "continue")

	out, err := handle(codexProbeInput(t, t.TempDir(), "printf 'agent-winglet-codex-probe\n'"))
	if err != nil {
		t.Fatalf("handle PostToolUse errored: %v", err)
	}
	if out == nil {
		t.Fatalf("expected probe output")
	}
	if out.Continue == nil || *out.Continue {
		t.Fatalf("Continue = %v, want pointer to false", out.Continue)
	}
	if out.Decision != "" || out.Reason != "" {
		t.Fatalf("continue probe should not emit decision/block fields: %+v", out)
	}
	if !strings.Contains(out.StopReason, "codex replacement probe (continue)") {
		t.Fatalf("StopReason = %q, want probe receipt", out.StopReason)
	}
	if out.SystemMessage != out.StopReason {
		t.Fatalf("SystemMessage = %q, want StopReason %q", out.SystemMessage, out.StopReason)
	}
	if out.HookSpecificOutput != nil {
		t.Fatalf("continue probe should not emit hookSpecificOutput, got %+v", out.HookSpecificOutput)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal errored: %v", err)
	}
	if got := string(encoded); !strings.Contains(got, `"continue":false`) || !strings.Contains(got, `"stopReason"`) {
		t.Fatalf("encoded continue probe output = %s, want continue:false and stopReason", got)
	}
}

func TestPostToolUseProbeBlockOutput(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(codexProbeEnvVar, "block")

	out, err := handle(codexProbeInput(t, t.TempDir(), "printf 'agent-winglet-codex-probe\n'"))
	if err != nil {
		t.Fatalf("handle PostToolUse errored: %v", err)
	}
	if out == nil {
		t.Fatalf("expected probe output")
	}
	if out.Continue != nil || out.StopReason != "" {
		t.Fatalf("block probe should not emit continue fields: %+v", out)
	}
	if out.Decision != "block" {
		t.Fatalf("Decision = %q, want block", out.Decision)
	}
	if !strings.Contains(out.Reason, "codex replacement probe (block)") {
		t.Fatalf("Reason = %q, want probe receipt", out.Reason)
	}
	if out.HookSpecificOutput == nil {
		t.Fatalf("expected hookSpecificOutput")
	}
	if out.HookSpecificOutput.HookEventName != "PostToolUse" {
		t.Fatalf("HookEventName = %q, want PostToolUse", out.HookSpecificOutput.HookEventName)
	}
	if out.HookSpecificOutput.AdditionalContext != out.Reason {
		t.Fatalf("AdditionalContext = %q, want Reason %q", out.HookSpecificOutput.AdditionalContext, out.Reason)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("Marshal errored: %v", err)
	}
	if got := string(encoded); !strings.Contains(got, `"decision":"block"`) || !strings.Contains(got, `"reason"`) {
		t.Fatalf("encoded block probe output = %s, want decision:block and reason", got)
	}
}

func TestPostToolUseProbeIgnoresUnmarkedCommand(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(codexProbeEnvVar, "continue")

	out, err := handle(codexProbeInput(t, t.TempDir(), "printf 'hello\n'"))
	if err != nil {
		t.Fatalf("handle PostToolUse errored: %v", err)
	}
	if out != nil {
		t.Fatalf("probe should ignore commands without marker, got %+v", out)
	}
}

func TestPostToolUseReadsCumulativeTokenDelta(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	rollout := writeRollout(t, []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"first","model":"gpt-5-codex"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cache_write_input_tokens":10}}}}`,
	})

	if _, err := handle(hookInput{SessionID: sessionID, Cwd: dir, HookEventName: "PostToolUse", TranscriptPath: rollout}); err != nil {
		t.Fatalf("first PostToolUse errored: %v", err)
	}
	appendRollout(t, rollout,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"second","model":"gpt-5-codex"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":180,"cache_write_input_tokens":20}}}}`,
	)
	if _, err := handle(hookInput{SessionID: sessionID, Cwd: dir, HookEventName: "Stop", TranscriptPath: rollout}); err != nil {
		t.Fatalf("Stop errored: %v", err)
	}

	s, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.TranscriptTokens != 200 {
		t.Fatalf("TranscriptTokens = %d, want latest cumulative 200", s.TranscriptTokens)
	}
	if s.TranscriptContentBytes != int64(len("first")+len("second")) {
		t.Fatalf("TranscriptContentBytes = %d, want %d", s.TranscriptContentBytes, len("first")+len("second"))
	}
}

func TestSessionEndReconcilesUsageWithoutReceiptWhenNoSuppression(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	sessionID := "codex-session"
	rollout := writeRollout(t, []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"hello","model":"gpt-5-codex"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","output":"tool output"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":120,"cached_input_tokens":80,"cache_write_input_tokens":5,"output_tokens":8,"reasoning_output_tokens":3,"total_tokens":133}}}}`,
	})

	out, err := handle(hookInput{
		SessionID:      sessionID,
		Cwd:            dir,
		HookEventName:  "SessionEnd",
		TranscriptPath: rollout,
	})
	if err != nil {
		t.Fatalf("SessionEnd errored: %v", err)
	}
	if out != nil {
		t.Fatalf("SessionEnd without suppression should not emit receipt, got %+v", out)
	}

	s, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.Agent != stats.AgentCodex {
		t.Fatalf("Agent = %q, want %q", s.Agent, stats.AgentCodex)
	}
	// input_tokens=120, cached_input_tokens=80 (subset of input_tokens),
	// cache_write_input_tokens=5 -> fresh input = 40, Tokens = 40+5 = 45.
	if s.TranscriptTokens != 45 {
		t.Fatalf("TranscriptTokens = %d, want 45", s.TranscriptTokens)
	}
	if s.TranscriptContentBytes != int64(len("hello")+len("tool output")) {
		t.Fatalf("TranscriptContentBytes = %d, want %d", s.TranscriptContentBytes, len("hello")+len("tool output"))
	}
}

func codexProbeInput(t *testing.T, dir, command string) hookInput {
	t.Helper()
	toolInput, err := json.Marshal(struct {
		Command string `json:"command"`
	}{Command: command})
	if err != nil {
		t.Fatalf("Marshal errored: %v", err)
	}
	return hookInput{
		SessionID:     "codex-probe-session",
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      "Bash",
		ToolInput:     toolInput,
	}
}

func codexBashPostInput(t *testing.T, dir, sessionID, command string, response interface{}) hookInput {
	t.Helper()
	return codexShellPostInput(t, dir, sessionID, "Bash", "command", command, response)
}

func codexShellPostInput(t *testing.T, dir, sessionID, toolName, commandField, command string, response interface{}) hookInput {
	t.Helper()
	toolInput, err := json.Marshal(map[string]string{commandField: command})
	if err != nil {
		t.Fatalf("Marshal tool input errored: %v", err)
	}
	toolResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal tool response errored: %v", err)
	}
	return hookInput{
		SessionID:     sessionID,
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      toolName,
		ToolInput:     toolInput,
		ToolResponse:  toolResponse,
	}
}

func codexLocalPostInput(t *testing.T, dir, sessionID, toolName string, input interface{}, response interface{}) hookInput {
	t.Helper()
	toolInput, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("Marshal tool input errored: %v", err)
	}
	toolResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal tool response errored: %v", err)
	}
	return hookInput{
		SessionID:     sessionID,
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      toolName,
		ToolInput:     toolInput,
		ToolResponse:  toolResponse,
	}
}

func codexApplyPatchInput(t *testing.T, dir, sessionID string) hookInput {
	t.Helper()
	toolResponse, err := json.Marshal("Done")
	if err != nil {
		t.Fatalf("Marshal tool response errored: %v", err)
	}
	return hookInput{
		SessionID:     sessionID,
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      "apply_patch",
		ToolInput:     json.RawMessage(`{"patch":"*** Begin Patch\n*** End Patch\n"}`),
		ToolResponse:  toolResponse,
	}
}
