package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

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
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
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

func TestSessionStartRegistersProjectAndResetsSession(t *testing.T) {
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

	reset, err := stats.LoadSession(dir, sessionID)
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if !reset.IsZero() || reset.TranscriptContentBytes != 0 {
		t.Fatalf("session was not reset: %+v", reset)
	}
}

func TestPostCompactPreservesCodexAgentForExistingUsage(t *testing.T) {
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
	if !got.IsZero() {
		t.Fatalf("mechanism counters should be reset after PostCompact: %+v", got)
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
	if s.TranscriptTokens != 110 {
		t.Fatalf("TranscriptTokens = %d, want 110", s.TranscriptTokens)
	}
	if s.TranscriptContentBytes != int64(len("hello")) {
		t.Fatalf("TranscriptContentBytes = %d, want %d", s.TranscriptContentBytes, len("hello"))
	}
	if s.TranscriptOffset == 0 {
		t.Fatalf("TranscriptOffset should advance")
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
	if s.TranscriptTokens != 125 {
		t.Fatalf("TranscriptTokens = %d, want 125", s.TranscriptTokens)
	}
	if s.TranscriptContentBytes != int64(len("hello")+len("tool output")) {
		t.Fatalf("TranscriptContentBytes = %d, want %d", s.TranscriptContentBytes, len("hello")+len("tool output"))
	}
}
