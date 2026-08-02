package main

import (
	"encoding/json"
	"testing"
)

func bashInput(t *testing.T, dir, sessionID, command string, response bashOutput) hookInput {
	t.Helper()
	toolInput, err := json.Marshal(struct {
		Command string `json:"command"`
	}{Command: command})
	if err != nil {
		t.Fatalf("marshal tool_input: %v", err)
	}
	toolResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal tool_response: %v", err)
	}
	return hookInput{
		SessionID:     sessionID,
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      "Bash",
		ToolInput:     toolInput,
		ToolResponse:  toolResponse,
	}
}

func TestHandleFirstBashCallPassesThrough(t *testing.T) {
	dir := t.TempDir()
	in := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out != nil {
		t.Fatalf("first-time Bash call should pass through untouched, got %+v", out)
	}
}

func TestHandleRepeatBashCallSubstitutes(t *testing.T) {
	dir := t.TempDir()
	in := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})

	if _, err := handle(in); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	out, err := handle(in)
	if err != nil {
		t.Fatalf("second call errored: %v", err)
	}
	if out == nil {
		t.Fatalf("repeat Bash call should produce a substitution, got nil")
	}
	updated, ok := out.HookSpecificOutput.UpdatedToolOutput.(bashOutput)
	if !ok {
		t.Fatalf("updatedToolOutput is not a bashOutput: %#v", out.HookSpecificOutput.UpdatedToolOutput)
	}
	if updated.Stdout == "hi\n" {
		t.Fatalf("substitution did not replace the original stdout")
	}
}

func TestHandleDifferentOutputIsNotSubstituted(t *testing.T) {
	dir := t.TempDir()
	first := bashInput(t, dir, "sess1", "date", bashOutput{Stdout: "Mon\n"})
	second := bashInput(t, dir, "sess1", "date", bashOutput{Stdout: "Tue\n"})

	if _, err := handle(first); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	out, err := handle(second)
	if err != nil {
		t.Fatalf("second call errored: %v", err)
	}
	if out != nil {
		t.Fatalf("changed output should not be substituted, got %+v", out)
	}
}

func TestHandleSkipsInterruptedIsImageAndStderr(t *testing.T) {
	dir := t.TempDir()
	cases := []bashOutput{
		{Stdout: "hi\n", Interrupted: true},
		{Stdout: "hi\n", IsImage: true},
		{Stdout: "hi\n", Stderr: "warning"},
	}
	for _, c := range cases {
		in := bashInput(t, dir, "sess1", "echo hi", c)
		if _, err := handle(in); err != nil {
			t.Fatalf("handle errored for %+v: %v", c, err)
		}
		out, err := handle(in)
		if err != nil {
			t.Fatalf("second handle errored for %+v: %v", c, err)
		}
		if out != nil {
			t.Fatalf("case %+v should never be substituted, got %+v", c, out)
		}
	}
}

func TestHandleIgnoresNonBashTools(t *testing.T) {
	dir := t.TempDir()
	in := hookInput{
		SessionID:     "sess1",
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      "Read",
		ToolInput:     json.RawMessage(`{"file_path":"/tmp/f"}`),
		ToolResponse:  json.RawMessage(`{"type":"text","file":{"content":"x"}}`),
	}
	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("non-Bash tool should never be substituted, got %+v", out)
	}
}

func TestHandleSessionStartInvalidatesLedger(t *testing.T) {
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("seeding call errored: %v", err)
	}

	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("SessionStart handling errored: %v", err)
	}

	out, err := handle(repeatIn)
	if err != nil {
		t.Fatalf("post-invalidation call errored: %v", err)
	}
	if out != nil {
		t.Fatalf("ledger entry survived SessionStart, got substitution %+v", out)
	}
}

func TestHandlePostCompactInvalidatesLedger(t *testing.T) {
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("seeding call errored: %v", err)
	}

	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "PostCompact"}); err != nil {
		t.Fatalf("PostCompact handling errored: %v", err)
	}

	out, err := handle(repeatIn)
	if err != nil {
		t.Fatalf("post-invalidation call errored: %v", err)
	}
	if out != nil {
		t.Fatalf("ledger entry survived PostCompact, got substitution %+v", out)
	}
}

func TestHandleDifferentSessionsAreIsolated(t *testing.T) {
	dir := t.TempDir()
	sess1 := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	sess2 := bashInput(t, dir, "sess2", "echo hi", bashOutput{Stdout: "hi\n"})

	if _, err := handle(sess1); err != nil {
		t.Fatalf("sess1 first call errored: %v", err)
	}
	out, err := handle(sess2)
	if err != nil {
		t.Fatalf("sess2 call errored: %v", err)
	}
	if out != nil {
		t.Fatalf("a different session_id should not see sess1's ledger, got %+v", out)
	}
}
