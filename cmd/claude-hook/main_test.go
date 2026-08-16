package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/umitkaanusta/agent-winglet/internal/config"
	"github.com/umitkaanusta/agent-winglet/internal/registry"
	"github.com/umitkaanusta/agent-winglet/internal/statedir"
	"github.com/umitkaanusta/agent-winglet/internal/stats"
)

// TestMain isolates HOME for the whole test binary: every state package this
// hook drives (ledger/phase/retire/stats via internal/statedir, plus
// registry/config) now resolves under $HOME rather than under the projectDir
// callers pass in, so without this every test in this file would read/write
// the real developer machine's ~/.agent-winglet on every run. Individual
// tests that assert on registry/config content still call their own
// t.Setenv("HOME", ...) for per-test isolation from each other; that
// per-test override wins for the duration of that test, same as it always
// has — this just supplies a safe default for tests that don't need that.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "agent-winglet-hook-test-home")
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

// linesOfApproxTokens returns 2*tokens single-character lines ("x",
// newline-joined, trailing newline included). budgetBody's estimatedTokens
// proxy is len(body)/4 (integer division); a body built this way is always
// exactly 4*tokens bytes long, so estimatedTokens(body) == tokens exactly —
// letting boundary tests target budgetTokenThreshold precisely instead of
// guessing through an arbitrary line count the way the old line-count
// threshold could be tested directly. The line count (2*tokens) is also
// always comfortably above budgetHeadLines+budgetTailLines, so these bodies
// never trip budgetBody's separate too-few-lines-to-split guard.
func linesOfApproxTokens(tokens int) string {
	lines := make([]string, 2*tokens)
	for i := range lines {
		lines[i] = "x"
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestEntitlementGateTellsClaudeWhenSignedOut(t *testing.T) {
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
	if out.HookSpecificOutput.AdditionalContext == "" {
		t.Fatalf("AdditionalContext should tell Claude the hook is gated")
	}
	if !strings.Contains(out.HookSpecificOutput.AdditionalContext, "AskUserQuestion") {
		t.Fatalf("AdditionalContext = %q, want an instruction to ask via AskUserQuestion", out.HookSpecificOutput.AdditionalContext)
	}
}

// TestEntitlementGateStaysBlockedAfterFirstNoticeInSameSession guards against
// a bug where the gate only actually blocked the first hook call of a
// session: entitlement.ShouldEmitNotice throttles the *visible* notice to
// once per session, but handle() used to treat "no notice to show" and
// "allowed" identically, so every call after the first quietly ran the full
// suppression logic — including recording state that later calls could
// then dedup-match against.
func TestEntitlementGateStaysBlockedAfterFirstNoticeInSameSession(t *testing.T) {
	t.Setenv("AGENT_WINGLET_TEST_ALLOW_UNGATED_HOOKS", "0")
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	if _, err := handle(hookInput{SessionID: "sess-gate", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatal(err)
	}

	in := bashInput(t, dir, "sess-gate", "echo hi", bashOutput{Stdout: "hi\n"})

	first, err := handle(in)
	if err != nil {
		t.Fatalf("first Bash call errored: %v", err)
	}
	if first == nil {
		t.Fatalf("blocked call should still return an output (with no notice), got nil")
	}
	if first.SystemMessage != "" {
		t.Fatalf("blocked call after the session's first notice shouldn't repeat it, got %q", first.SystemMessage)
	}
	if first.HookSpecificOutput.AdditionalContext != "" {
		t.Fatalf("blocked call after the session's first notice shouldn't re-nudge AskUserQuestion either, got %q", first.HookSpecificOutput.AdditionalContext)
	}
	if _, ok := first.HookSpecificOutput.UpdatedToolOutput.(bashOutput); ok {
		t.Fatalf("blocked session should never produce a dedup substitution, got %+v", first)
	}

	second, err := handle(in)
	if err != nil {
		t.Fatalf("second Bash call errored: %v", err)
	}
	if second == nil {
		t.Fatalf("blocked call should still return an output, got nil")
	}
	if _, ok := second.HookSpecificOutput.UpdatedToolOutput.(bashOutput); ok {
		t.Fatalf("still-blocked session substituted a repeat call — the gate stopped applying after the first blocked call: %+v", second)
	}
}

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
	t.Setenv("HOME", t.TempDir())
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

func TestHandleSessionStartRegistersProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("SessionStart handling errored: %v", err)
	}

	dirs, err := registry.Load()
	if err != nil {
		t.Fatalf("registry.Load errored: %v", err)
	}
	if len(dirs) != 1 || dirs[0] != dir {
		t.Fatalf("expected project registered after SessionStart, got %v", dirs)
	}
}

func TestHandlePostCompactInvalidatesLedger(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
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

func TestBudgetStdoutThresholdBoundary(t *testing.T) {
	cases := []struct {
		name       string
		tokens     int
		wantBudget bool
	}{
		{"just under threshold", budgetTokenThreshold - 1, false},
		{"at threshold", budgetTokenThreshold, false},
		{"just over threshold", budgetTokenThreshold + 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, ok, err := budgetStdout(linesOfApproxTokens(c.tokens), t.TempDir(), "sess1")
			if err != nil {
				t.Fatalf("budgetStdout errored: %v", err)
			}
			if ok != c.wantBudget {
				t.Fatalf("%d tokens: budgetStdout ok = %v, want %v", c.tokens, ok, c.wantBudget)
			}
		})
	}
}

func TestHandleBudgetsLongFirstTimeOutput(t *testing.T) {
	dir := t.TempDir()
	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out == nil {
		t.Fatalf("long first-time output should be budgeted, got nil")
	}
	updated, ok := out.HookSpecificOutput.UpdatedToolOutput.(bashOutput)
	if !ok {
		t.Fatalf("updatedToolOutput is not a bashOutput: %#v", out.HookSpecificOutput.UpdatedToolOutput)
	}
	if updated.Stdout == longOutput {
		t.Fatalf("budgeting did not replace the original stdout")
	}
	if !strings.Contains(updated.Stdout, "lines omitted") {
		t.Fatalf("budgeted output missing omission marker: %q", updated.Stdout)
	}
}

// TestHandleBashBudgetArchivesFullOutputForRecovery guards the gap plain
// head/tail truncation used to leave open: if the agent actually needed
// something from the dropped middle, its only recovery was re-running the
// same Bash command. Budgeting now archives the full pre-trim stdout the
// same way handleRetireInvestigate archives investigate output, and names
// the path in the notice line — this asserts that path is present and
// reading it back returns the exact original stdout.
func TestHandleBashBudgetArchivesFullOutputForRecovery(t *testing.T) {
	dir := t.TempDir()
	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	updated := out.HookSpecificOutput.UpdatedToolOutput.(bashOutput)

	idx := strings.Index(updated.Stdout, "full output at ")
	if idx == -1 {
		t.Fatalf("budgeted stdout missing an archive path, got %q", updated.Stdout)
	}
	path := updated.Stdout[idx+len("full output at "):]
	if nl := strings.IndexByte(path, '\n'); nl != -1 {
		path = path[:nl]
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading archived output at %q errored: %v", path, err)
	}
	if string(stored) != longOutput {
		t.Fatalf("archived output = %q, want the original unbudgeted stdout %q", stored, longOutput)
	}
}

func TestHandleDoesNotBudgetShortFirstTimeOutput(t *testing.T) {
	dir := t.TempDir()
	in := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: linesOfApproxTokens(budgetTokenThreshold)})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out != nil {
		t.Fatalf("output at/under threshold should pass through untouched, got %+v", out)
	}
}

func TestHandleDoesNotBudgetFailedInterruptedOrImageOutput(t *testing.T) {
	dir := t.TempDir()
	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	cases := []bashOutput{
		{Stdout: longOutput, Stderr: "build failed"},
		{Stdout: longOutput, Interrupted: true},
		{Stdout: longOutput, IsImage: true},
	}
	for _, c := range cases {
		in := bashInput(t, dir, "sess1", "go build ./...", c)
		out, err := handle(in)
		if err != nil {
			t.Fatalf("handle errored for %+v: %v", c, err)
		}
		if out != nil {
			t.Fatalf("case %+v should never be budgeted, got %+v", c, out)
		}
	}
}

func TestHandleRepeatCheckTakesPrecedenceOverBudgeting(t *testing.T) {
	dir := t.TempDir()
	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})

	first, err := handle(in)
	if err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if first == nil || !strings.Contains(first.HookSpecificOutput.UpdatedToolOutput.(bashOutput).Stdout, "lines omitted") {
		t.Fatalf("first call should be budgeted, got %+v", first)
	}

	second, err := handle(in)
	if err != nil {
		t.Fatalf("second call errored: %v", err)
	}
	if second == nil {
		t.Fatalf("repeat of a budgeted command should still be substituted, got nil")
	}
	updated := second.HookSpecificOutput.UpdatedToolOutput.(bashOutput)
	if !strings.HasPrefix(updated.Stdout, "[agent-winglet] unchanged since turn") {
		t.Fatalf("repeat should use the 'unchanged since turn N' message, not a budget receipt, got %q", updated.Stdout)
	}
}

func linesOfEntries(n int) []string {
	entries := make([]string, n)
	for i := range entries {
		entries[i] = fmt.Sprintf("file%d.go", i)
	}
	return entries
}

// entriesOfApproxTokens returns 2*tokens+1 single-character entries. Joined
// by budgetEntryList with "\n" (the same join budgetBody's caller performs)
// that's exactly 4*tokens+1 bytes — one more than linesOfApproxTokens'
// 4*tokens because strings.Join has no trailing separator — but integer
// division still floors len/4 to exactly tokens, so this hits the same
// token-threshold boundary precisely for the entry-list shape (Glob's
// filenames) that linesOfApproxTokens hits for freeform text bodies.
func entriesOfApproxTokens(tokens int) []string {
	entries := make([]string, 2*tokens+1)
	for i := range entries {
		entries[i] = "x"
	}
	return entries
}

func grepInput(t *testing.T, dir, sessionID string, response grepResponse) hookInput {
	t.Helper()
	toolResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal tool_response: %v", err)
	}
	return hookInput{
		SessionID:     sessionID,
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      "Grep",
		ToolInput:     json.RawMessage(`{"pattern":"TODO"}`),
		ToolResponse:  toolResponse,
	}
}

func globInput(t *testing.T, dir, sessionID string, response globResponse) hookInput {
	t.Helper()
	toolResponse, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal tool_response: %v", err)
	}
	return hookInput{
		SessionID:     sessionID,
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      "Glob",
		ToolInput:     json.RawMessage(`{"pattern":"**/*.go"}`),
		ToolResponse:  toolResponse,
	}
}

func TestBudgetTextFieldThresholdBoundary(t *testing.T) {
	cases := []struct {
		name       string
		tokens     int
		wantBudget bool
	}{
		{"just under threshold", budgetTokenThreshold - 1, false},
		{"at threshold", budgetTokenThreshold, false},
		{"just over threshold", budgetTokenThreshold + 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, ok, err := budgetTextField(linesOfApproxTokens(c.tokens), t.TempDir(), "sess1")
			if err != nil {
				t.Fatalf("budgetTextField errored: %v", err)
			}
			if ok != c.wantBudget {
				t.Fatalf("%d tokens: budgetTextField ok = %v, want %v", c.tokens, ok, c.wantBudget)
			}
		})
	}
}

func TestBudgetEntryListThresholdBoundary(t *testing.T) {
	cases := []struct {
		name       string
		tokens     int
		wantBudget bool
	}{
		{"just under threshold", budgetTokenThreshold - 1, false},
		{"at threshold", budgetTokenThreshold, false},
		{"just over threshold", budgetTokenThreshold + 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, _, ok, err := budgetEntryList(entriesOfApproxTokens(c.tokens), t.TempDir(), "sess1")
			if err != nil {
				t.Fatalf("budgetEntryList errored: %v", err)
			}
			if ok != c.wantBudget {
				t.Fatalf("%d tokens: budgetEntryList ok = %v, want %v", c.tokens, ok, c.wantBudget)
			}
		})
	}
}

func TestHandleBudgetsLongGrepContent(t *testing.T) {
	dir := t.TempDir()
	longContent := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := grepInput(t, dir, "sess1", grepResponse{Mode: "content", NumFiles: 3, Content: longContent})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out == nil {
		t.Fatalf("long Grep content should be budgeted, got nil")
	}
	updated, ok := out.HookSpecificOutput.UpdatedToolOutput.(grepResponse)
	if !ok {
		t.Fatalf("updatedToolOutput is not a grepResponse: %#v", out.HookSpecificOutput.UpdatedToolOutput)
	}
	if updated.Content == longContent {
		t.Fatalf("budgeting did not replace the original content")
	}
	if !strings.Contains(updated.Content, "lines omitted") {
		t.Fatalf("budgeted content missing omission marker: %q", updated.Content)
	}
	// Fields other than Content must survive the round-trip untouched.
	if updated.NumFiles != 3 || updated.Mode != "content" {
		t.Fatalf("budgeting dropped other fields, got %+v", updated)
	}

	idx := strings.Index(updated.Content, "full output at ")
	if idx == -1 {
		t.Fatalf("budgeted content missing an archive path, got %q", updated.Content)
	}
	path := updated.Content[idx+len("full output at "):]
	if nl := strings.IndexByte(path, '\n'); nl != -1 {
		path = path[:nl]
	}
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading archived content at %q errored: %v", path, err)
	}
	if string(stored) != longContent {
		t.Fatalf("archived content = %q, want the original unbudgeted content %q", stored, longContent)
	}
}

func TestHandleDoesNotBudgetShortGrepContent(t *testing.T) {
	dir := t.TempDir()
	in := grepInput(t, dir, "sess1", grepResponse{Mode: "content", Content: linesOfApproxTokens(budgetTokenThreshold)})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out != nil {
		t.Fatalf("content at/under threshold should pass through untouched, got %+v", out)
	}
}

func TestHandleDoesNotBudgetGrepFilesWithMatchesMode(t *testing.T) {
	dir := t.TempDir()
	// files_with_matches/count mode carries no Content — just filenames and
	// counts, already compact — so there's nothing for this path to budget.
	in := grepInput(t, dir, "sess1", grepResponse{Mode: "files_with_matches", NumFiles: 200, Filenames: linesOfEntries(200)})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out != nil {
		t.Fatalf("files_with_matches mode (no Content field) should pass through untouched, got %+v", out)
	}
}

func TestHandleBudgetsLongGlobFilenames(t *testing.T) {
	dir := t.TempDir()
	longFilenames := entriesOfApproxTokens(budgetTokenThreshold + 1)
	in := globInput(t, dir, "sess1", globResponse{NumFiles: len(longFilenames), Filenames: longFilenames})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out == nil {
		t.Fatalf("long Glob filenames should be budgeted, got nil")
	}
	updated, ok := out.HookSpecificOutput.UpdatedToolOutput.(globResponse)
	if !ok {
		t.Fatalf("updatedToolOutput is not a globResponse: %#v", out.HookSpecificOutput.UpdatedToolOutput)
	}
	if len(updated.Filenames) >= len(longFilenames) {
		t.Fatalf("budgeting did not shrink the filenames array: got %d entries, want fewer than %d", len(updated.Filenames), len(longFilenames))
	}
	found := false
	for _, f := range updated.Filenames {
		if strings.Contains(f, "entries omitted") {
			found = true
		}
	}
	if !found {
		t.Fatalf("budgeted filenames missing omission marker entry: %v", updated.Filenames)
	}

	var archiveEntry string
	for _, f := range updated.Filenames {
		if strings.Contains(f, "full list at ") {
			archiveEntry = f
		}
	}
	if archiveEntry == "" {
		t.Fatalf("budgeted filenames missing an archive path entry: %v", updated.Filenames)
	}
	path := archiveEntry[strings.Index(archiveEntry, "full list at ")+len("full list at "):]
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading archived filenames at %q errored: %v", path, err)
	}
	if string(stored) != strings.Join(longFilenames, "\n") {
		t.Fatalf("archived filenames don't match the original list")
	}
}

func TestHandleDoesNotBudgetShortGlobFilenames(t *testing.T) {
	dir := t.TempDir()
	in := globInput(t, dir, "sess1", globResponse{Filenames: entriesOfApproxTokens(budgetTokenThreshold)})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out != nil {
		t.Fatalf("filenames at/under threshold should pass through untouched, got %+v", out)
	}
}

func TestHandleWebSearchIsNeverBudgeted(t *testing.T) {
	dir := t.TempDir()
	// WebSearch is deliberately excluded (see handlePostToolUse's default
	// case): its results array has no single freeform field to budget.
	// Regression guard: a long tool_response must still pass through
	// untouched rather than erroring or being silently mangled.
	longResponse := json.RawMessage(`{"query":"x","results":[{"tool_use_id":"t1","content":[{"title":"a","url":"https://a"}]},"` + linesOfApproxTokens(budgetTokenThreshold+1) + `"],"durationSeconds":1,"searchCount":1}`)
	in := hookInput{
		SessionID:     "sess1",
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      "WebSearch",
		ToolInput:     json.RawMessage(`{"query":"x"}`),
		ToolResponse:  longResponse,
	}

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out != nil {
		t.Fatalf("WebSearch should never be budgeted, got %+v", out)
	}
}

func TestHandleGrepBudgetingDoesNotFirePostBoundary(t *testing.T) {
	dir := t.TempDir()
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}

	// Post-boundary, a long Grep content field must go through retirement
	// (a string receipt), never through budgeting (a grepResponse) — the two
	// mechanisms are mutually exclusive by construction.
	longContent := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := grepInput(t, dir, "sess1", grepResponse{Mode: "content", Content: longContent})
	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	if out == nil {
		t.Fatalf("post-boundary investigate call should be retired, got nil")
	}
	if _, ok := out.HookSpecificOutput.UpdatedToolOutput.(string); !ok {
		t.Fatalf("post-boundary Grep should be retired (string receipt), not budgeted, got %#v", out.HookSpecificOutput.UpdatedToolOutput)
	}
}

func TestHandleGrepBudgetTrimRecordsStat(t *testing.T) {
	dir := t.TempDir()
	longContent := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := grepInput(t, dir, "sess1", grepResponse{Mode: "content", Content: longContent})
	if _, err := handle(in); err != nil {
		t.Fatalf("handle errored: %v", err)
	}

	s, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.BudgetTrims != 1 {
		t.Fatalf("BudgetTrims = %d, want 1", s.BudgetTrims)
	}
	if s.BudgetBytesOmitted <= 0 {
		t.Fatalf("BudgetBytesOmitted = %d, want positive", s.BudgetBytesOmitted)
	}
}

func toolCallInput(sessionID, dir, toolName string) hookInput {
	return hookInput{
		SessionID:     sessionID,
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      toolName,
		ToolInput:     json.RawMessage(`{}`),
		ToolResponse:  json.RawMessage(`{}`),
	}
}

func TestHandlePhaseBoundaryFiresOnFirstImplementAfterInvestigate(t *testing.T) {
	dir := t.TempDir()

	if out, err := handle(toolCallInput("sess1", dir, "Read")); err != nil || out != nil {
		t.Fatalf("investigate call should pass through untouched, got out=%+v err=%v", out, err)
	}

	out, err := handle(toolCallInput("sess1", dir, "Edit"))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil {
		t.Fatalf("first Edit after a Read should fire the boundary suggestion, got nil")
	}
	if out.SystemMessage == "" {
		t.Fatalf("expected a systemMessage, got %+v", out)
	}
	if out.HookSpecificOutput.AdditionalContext == "" {
		t.Fatalf("expected additionalContext, got %+v", out)
	}
}

func TestHandlePhaseBoundaryDoesNotFireWithoutPriorInvestigate(t *testing.T) {
	dir := t.TempDir()

	out, err := handle(toolCallInput("sess1", dir, "Edit"))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("Edit with no prior investigate call should not fire, got %+v", out)
	}
}

func TestHandlePhaseBoundaryFiresOnlyOnce(t *testing.T) {
	dir := t.TempDir()

	if _, err := handle(toolCallInput("sess1", dir, "Grep")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	first, err := handle(toolCallInput("sess1", dir, "Write"))
	if err != nil {
		t.Fatalf("first Write errored: %v", err)
	}
	if first == nil {
		t.Fatalf("first Write after Grep should fire, got nil")
	}

	second, err := handle(toolCallInput("sess1", dir, "Edit"))
	if err != nil {
		t.Fatalf("second implement call errored: %v", err)
	}
	if second != nil {
		t.Fatalf("boundary suggestion should fire at most once per session, got %+v", second)
	}
}

func TestHandlePhaseBoundaryIgnoresUnclassifiedTools(t *testing.T) {
	dir := t.TempDir()
	// Bash is deliberately unclassified: it should neither count as
	// investigate nor as the implement call that crosses the boundary.
	if out, err := handle(toolCallInput("sess1", dir, "Bash")); err != nil || out != nil {
		t.Fatalf("unclassified tool should pass through untouched, got out=%+v err=%v", out, err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	out, err := handle(toolCallInput("sess1", dir, "Bash"))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("Bash should never itself cross the boundary, got %+v", out)
	}
}

func TestHandlePhaseBoundaryResetsOnSessionStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("SessionStart handling errored: %v", err)
	}

	out, err := handle(toolCallInput("sess1", dir, "Edit"))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("Edit after SessionStart should not fire — prior investigate call should be forgotten, got %+v", out)
	}
}

// TestInvestigateCallThresholdResetsOnSessionStart covers spec-nextsteps.md's
// §4 alongside the SessionStart/PostCompact invalidation guarantee every
// other mechanism already has: a session that reaches investigateCallThreshold,
// then restarts, should not carry that count into the new session — no
// substitute for state survives a restart.
func TestInvestigateCallThresholdResetsOnSessionStart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	for i := 0; i < investigateCallThreshold; i++ {
		if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
			t.Fatalf("seeding call %d errored: %v", i+1, err)
		}
	}
	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("SessionStart handling errored: %v", err)
	}

	out, err := handle(toolCallInput("sess1", dir, "Read"))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("investigate call after SessionStart should not be retired — count should be forgotten, got %+v", out)
	}
}

func TestHandlePhaseBoundaryResetsOnPostCompact(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()

	if _, err := handle(toolCallInput("sess1", dir, "Grep")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("first crossing errored: %v", err)
	}
	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "PostCompact"}); err != nil {
		t.Fatalf("PostCompact handling errored: %v", err)
	}

	if _, err := handle(toolCallInput("sess1", dir, "WebSearch")); err != nil {
		t.Fatalf("post-compact investigate call errored: %v", err)
	}
	out, err := handle(toolCallInput("sess1", dir, "NotebookEdit"))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil {
		t.Fatalf("boundary suggestion should be able to fire again after PostCompact, got nil")
	}
}

func investigateInput(sessionID, dir, toolName string, toolInput, toolResponse json.RawMessage) hookInput {
	return hookInput{
		SessionID:     sessionID,
		Cwd:           dir,
		HookEventName: "PostToolUse",
		ToolName:      toolName,
		ToolInput:     toolInput,
		ToolResponse:  toolResponse,
	}
}

func TestHandleDoesNotRetireInvestigateBeforeBoundaryCrossed(t *testing.T) {
	dir := t.TempDir()
	in := investigateInput("sess1", dir, "Grep",
		json.RawMessage(`{"pattern":"TODO"}`), json.RawMessage(`{"matches":["a.go:1"]}`))

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("investigate call before any crossing should pass through untouched, got %+v", out)
	}
}

func TestHandleRetiresInvestigateAfterBoundaryCrossed(t *testing.T) {
	dir := t.TempDir()

	// Seed: one investigate call, then the implement call that crosses the
	// boundary (fires the compact suggestion, consumed here).
	if _, err := handle(investigateInput("sess1", dir, "Read",
		json.RawMessage(`{"file_path":"/tmp/a.go"}`), json.RawMessage(`{"type":"text","file":{"content":"package a"}}`))); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}

	response := json.RawMessage(`{"matches":["b.go:1: TODO fix this"]}`)
	in := investigateInput("sess1", dir, "Grep", json.RawMessage(`{"pattern":"TODO"}`), response)

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil {
		t.Fatalf("investigate call after the boundary has crossed should be retired, got nil")
	}
	receipt, ok := out.HookSpecificOutput.UpdatedToolOutput.(string)
	if !ok {
		t.Fatalf("updatedToolOutput is not a string receipt: %#v", out.HookSpecificOutput.UpdatedToolOutput)
	}
	if !strings.Contains(receipt, "TODO") {
		t.Fatalf("receipt missing the extracted key, got %q", receipt)
	}
	if !strings.Contains(receipt, "retired post-boundary") {
		t.Fatalf("receipt missing the expected marker, got %q", receipt)
	}

	// The full content must still be recoverable from the path in the receipt.
	idx := strings.LastIndex(receipt, "full content at ")
	if idx == -1 {
		t.Fatalf("receipt missing a content path, got %q", receipt)
	}
	path := receipt[idx+len("full content at "):]
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading retired content at %q errored: %v", path, err)
	}
	if string(stored) != string(response) {
		t.Fatalf("retired content = %q, want %q", stored, response)
	}
}

func TestHandleRetireIsNotBashOnly(t *testing.T) {
	dir := t.TempDir()
	if _, err := handle(toolCallInput("sess1", dir, "Glob")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Write")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}

	for _, tool := range []string{"WebFetch", "WebSearch", "Task"} {
		out, err := handle(toolCallInput("sess1", dir, tool))
		if err != nil {
			t.Fatalf("%s errored: %v", tool, err)
		}
		if out == nil {
			t.Fatalf("%s after the boundary crossed should be retired, got nil", tool)
		}
		if _, ok := out.HookSpecificOutput.UpdatedToolOutput.(string); !ok {
			t.Fatalf("%s: updatedToolOutput is not a string receipt: %#v", tool, out.HookSpecificOutput.UpdatedToolOutput)
		}
	}
}

// TestHandleDoesNotRetireInvestigateAtOrBelowCallThreshold covers
// spec-nextsteps.md's §4: the first investigateCallThreshold
// investigate-classified calls this session, even with no implement call in
// sight (boundary never crosses), should all pass through untouched — the
// threshold only fires on the call that pushes the count past it.
func TestHandleDoesNotRetireInvestigateAtOrBelowCallThreshold(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < investigateCallThreshold; i++ {
		out, err := handle(toolCallInput("sess1", dir, "Read"))
		if err != nil {
			t.Fatalf("call %d errored: %v", i+1, err)
		}
		if out != nil {
			t.Fatalf("call %d (at or below threshold) should pass through untouched, got %+v", i+1, out)
		}
	}
}

// TestHandleRetiresInvestigateAfterCallThresholdExceededPreBoundary covers
// spec-nextsteps.md's §4: once a session has made more than
// investigateCallThreshold investigate calls — with the investigate→
// implement boundary never crossed — every further investigate call is
// retired outright, the same archive-and-receipt treatment
// handleRetireInvestigate already gives post-boundary calls, distinguished
// only by the reason in the receipt.
func TestHandleRetiresInvestigateAfterCallThresholdExceededPreBoundary(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < investigateCallThreshold; i++ {
		if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
			t.Fatalf("seeding call %d errored: %v", i+1, err)
		}
	}

	response := json.RawMessage(`{"matches":["b.go:1: TODO fix this"]}`)
	in := investigateInput("sess1", dir, "Grep", json.RawMessage(`{"pattern":"TODO"}`), response)

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil {
		t.Fatalf("investigate call past the threshold should be retired, got nil")
	}
	receipt, ok := out.HookSpecificOutput.UpdatedToolOutput.(string)
	if !ok {
		t.Fatalf("updatedToolOutput is not a string receipt: %#v", out.HookSpecificOutput.UpdatedToolOutput)
	}
	if !strings.Contains(receipt, "TODO") {
		t.Fatalf("receipt missing the extracted key, got %q", receipt)
	}
	if !strings.Contains(receipt, investigateThresholdReason) {
		t.Fatalf("receipt missing the expected threshold marker, got %q", receipt)
	}
	if strings.Contains(receipt, "post-boundary") {
		t.Fatalf("threshold-triggered receipt should not claim post-boundary, got %q", receipt)
	}

	idx := strings.LastIndex(receipt, "full content at ")
	if idx == -1 {
		t.Fatalf("receipt missing a content path, got %q", receipt)
	}
	path := receipt[idx+len("full content at "):]
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading retired content at %q errored: %v", path, err)
	}
	if string(stored) != string(response) {
		t.Fatalf("retired content = %q, want %q", stored, response)
	}
}

func TestHandleNeverRetiresImplementOrBashCalls(t *testing.T) {
	dir := t.TempDir()
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}

	// A second implement call, post-boundary, should never be treated as
	// retirable — retirement only ever applies to investigate-classified
	// tools.
	out, err := handle(toolCallInput("sess1", dir, "Write"))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("implement call should never be retired, got %+v", out)
	}

	// Bash is unclassified, so it never goes through handleRetireInvestigate
	// (that path only ever fires for investigateTools). It does get its own
	// post-boundary retirement for long output (see
	// TestHandleRetiresLongBashOutputPostBoundary) — but short output like
	// this should still pass through untouched, same as pre-boundary.
	bashIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	out, err = handle(bashIn)
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("short first-time Bash call post-boundary should still pass through untouched, got %+v", out)
	}
}

// TestHandleRetiresLongBashOutputPostBoundary covers spec-nextsteps.md's
// item 3: post-boundary, a long first-time Bash call should be fully
// retired (archived + compact receipt), not just head/tail budgeted —
// matching the recovery guarantee handleRetireInvestigate already gives
// investigate-classified tools.
func TestHandleRetiresLongBashOutputPostBoundary(t *testing.T) {
	dir := t.TempDir()
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}

	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil {
		t.Fatalf("long first-time Bash output post-boundary should be retired, got nil")
	}
	updated, ok := out.HookSpecificOutput.UpdatedToolOutput.(bashOutput)
	if !ok {
		t.Fatalf("updatedToolOutput is not a bashOutput: %#v", out.HookSpecificOutput.UpdatedToolOutput)
	}
	if !strings.Contains(updated.Stdout, "retired post-boundary") {
		t.Fatalf("receipt missing the expected marker, got %q", updated.Stdout)
	}
	if strings.Contains(updated.Stdout, "lines omitted") {
		t.Fatalf("post-boundary long output should be fully retired, not head/tail budgeted, got %q", updated.Stdout)
	}

	idx := strings.Index(updated.Stdout, "full output at ")
	if idx == -1 {
		t.Fatalf("receipt missing an archive path, got %q", updated.Stdout)
	}
	path := updated.Stdout[idx+len("full output at "):]
	stored, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading archived output at %q errored: %v", path, err)
	}
	if string(stored) != longOutput {
		t.Fatalf("archived output = %q, want the original stdout %q", stored, longOutput)
	}
}

// TestHandleBashDedupTakesPrecedenceOverRetirementPostBoundary asserts an
// exact-repeat Bash call post-boundary still hits the ledger's cheap
// "unchanged since turn N" substitution instead of being archived — the
// content already appeared once in the transcript, so there's nothing new
// worth retiring to disk.
func TestHandleBashDedupTakesPrecedenceOverRetirementPostBoundary(t *testing.T) {
	dir := t.TempDir()
	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})

	if _, err := handle(in); err != nil {
		t.Fatalf("seeding first call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil {
		t.Fatalf("repeat Bash call post-boundary should still be deduped, got nil")
	}
	updated := out.HookSpecificOutput.UpdatedToolOutput.(bashOutput)
	if !strings.Contains(updated.Stdout, "unchanged since turn") {
		t.Fatalf("expected a dedup receipt, got %q", updated.Stdout)
	}
	if strings.Contains(updated.Stdout, "retired post-boundary") {
		t.Fatalf("repeat call should be deduped, not retired, got %q", updated.Stdout)
	}
}

// TestHandleSessionStartInvalidatesBashRetireArchive covers the same "hard
// constraint" as TestHandleSessionStartInvalidatesBudgetArchive, for the new
// post-boundary Bash retire path's own retire.Store call.
func TestHandleSessionStartInvalidatesBashRetireArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}

	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})
	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	updated := out.HookSpecificOutput.UpdatedToolOutput.(bashOutput)
	idx := strings.Index(updated.Stdout, "full output at ")
	path := updated.Stdout[idx+len("full output at "):]

	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("SessionStart handling errored: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("bash retire archive survived SessionStart: err=%v", err)
	}
}

func TestHandleSessionStartInvalidatesRetiredContent(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}
	response := json.RawMessage(`{"matches":["x"]}`)
	in := investigateInput("sess1", dir, "Grep", json.RawMessage(`{"pattern":"x"}`), response)
	out, err := handle(in)
	if err != nil {
		t.Fatalf("retiring call errored: %v", err)
	}
	if out == nil {
		t.Fatalf("expected the Grep call to be retired, got nil")
	}

	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("SessionStart handling errored: %v", err)
	}

	receipt := out.HookSpecificOutput.UpdatedToolOutput.(string)
	idx := strings.LastIndex(receipt, "full content at ")
	path := receipt[idx+len("full content at "):]
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("retired content survived SessionStart: err=%v", err)
	}

	// And the boundary itself must be forgotten too: the same Grep call,
	// right after SessionStart, should no longer be retired.
	out, err = handle(in)
	if err != nil {
		t.Fatalf("post-invalidation call errored: %v", err)
	}
	if out != nil {
		t.Fatalf("investigate call after SessionStart should not be retired (boundary forgotten), got %+v", out)
	}
}

// TestHandleSessionStartInvalidatesBudgetArchive covers the same "hard
// constraint" (see the package doc's SessionStart/PostCompact entry) for
// budgeting's new archive-for-recovery path as
// TestHandleSessionStartInvalidatesRetiredContent already covers for
// handleRetireInvestigate — both go through retire.Store/Invalidate, but
// this is a distinct call site added later, so it gets its own regression
// guard rather than assuming coverage transfers.
func TestHandleSessionStartInvalidatesBudgetArchive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle returned error: %v", err)
	}
	updated := out.HookSpecificOutput.UpdatedToolOutput.(bashOutput)
	idx := strings.Index(updated.Stdout, "full output at ")
	if idx == -1 {
		t.Fatalf("budgeted stdout missing an archive path, got %q", updated.Stdout)
	}
	path := updated.Stdout[idx+len("full output at "):]
	if nl := strings.IndexByte(path, '\n'); nl != -1 {
		path = path[:nl]
	}

	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("SessionStart handling errored: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("budget archive survived SessionStart: err=%v", err)
	}
}

func sessionEndInput(sessionID, dir string) hookInput {
	return hookInput{SessionID: sessionID, Cwd: dir, HookEventName: "SessionEnd"}
}

func TestHandleSessionEndZeroActivityEmitsNothing(t *testing.T) {
	dir := t.TempDir()
	out, err := handle(sessionEndInput("sess1", dir))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("a session with no mechanism activity should emit no receipt, got %+v", out)
	}
}

func TestHandleSessionEndReportsDedupHit(t *testing.T) {
	t.Setenv(quietEnvVar, "0")
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	out, err := handle(sessionEndInput("sess1", dir))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil || out.SystemMessage == "" {
		t.Fatalf("expected a receipt systemMessage, got %+v", out)
	}
	if !strings.Contains(out.SystemMessage, "repeat command") {
		t.Fatalf("receipt missing dedup mention, got %q", out.SystemMessage)
	}
	if !strings.Contains(out.SystemMessage, "not a validated usage or cost figure") {
		t.Fatalf("receipt missing the honesty parenthetical, got %q", out.SystemMessage)
	}
}

func TestHandleSessionEndReportsBudgetTrim(t *testing.T) {
	t.Setenv(quietEnvVar, "0")
	dir := t.TempDir()
	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})
	if _, err := handle(in); err != nil {
		t.Fatalf("budgeted call errored: %v", err)
	}

	out, err := handle(sessionEndInput("sess1", dir))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "trimmed") {
		t.Fatalf("expected a receipt mentioning trimming, got %+v", out)
	}
}

func TestHandleSessionEndReportsRetiredCall(t *testing.T) {
	t.Setenv(quietEnvVar, "0")
	dir := t.TempDir()
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Grep")); err != nil {
		t.Fatalf("post-boundary investigate call errored: %v", err)
	}

	out, err := handle(sessionEndInput("sess1", dir))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "retired post-boundary") {
		t.Fatalf("expected a receipt mentioning retirement, got %+v", out)
	}
}

// TestHandlePostToolUseTracksTranscriptUsageLive is the regression test for
// the bug report this fix addresses: a session's transcript-derived usage
// (tokens/cost/content bytes — what the desktop app's %-saved and $-saved
// figures are computed from) used to only ever get read once, at
// SessionEnd, which meant an in-progress session always looked identical to
// an untouched one no matter how long it ran or how fast the app polled.
// This asserts the per-session stats file already carries real, nonzero
// transcript usage mid-session — before any SessionEnd call — and that a
// second PostToolUse call after the transcript grows further adds to that
// total rather than losing or re-double-counting the first call's data.
func TestHandlePostToolUseTracksTranscriptUsageLive(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := writeTranscriptFixture(t, dir)

	first := bashInput(t, dir, "sess1", "echo one", bashOutput{Stdout: "one\n"})
	first.TranscriptPath = transcriptPath
	if _, err := handle(first); err != nil {
		t.Fatalf("first call errored: %v", err)
	}

	sess, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if sess.TranscriptTokens == 0 || sess.TranscriptCostUSD == 0 || sess.TranscriptContentBytes == 0 {
		t.Fatalf("expected nonzero transcript usage after a single PostToolUse call, mid-session (no SessionEnd yet), got %+v", sess)
	}
	firstTokens := sess.TranscriptTokens

	// Simulate the transcript growing between tool calls (another assistant
	// turn lands) and a second PostToolUse call — the delta must add on top
	// of the first call's total, not replace or double-count it.
	f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile for append errored: %v", err)
	}
	extraLine := `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":40,"output_tokens":10}}}` + "\n"
	if _, err := f.WriteString(extraLine); err != nil {
		t.Fatalf("append WriteString errored: %v", err)
	}
	f.Close()

	second := bashInput(t, dir, "sess1", "echo two", bashOutput{Stdout: "two\n"})
	second.TranscriptPath = transcriptPath
	if _, err := handle(second); err != nil {
		t.Fatalf("second call errored: %v", err)
	}

	sess2, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("second LoadSession errored: %v", err)
	}
	if sess2.TranscriptTokens != firstTokens+40 {
		t.Fatalf("TranscriptTokens after second call = %d, want %d (first call's %d + the new line's 40 input tokens)",
			sess2.TranscriptTokens, firstTokens+40, firstTokens)
	}
}

// TestHandleSessionEndThenResumeDoesNotDoubleCountTranscriptUsage is the
// regression test for the bug SetTranscriptUsage's offset parameter fixes:
// SessionEnd's full reconciliation read used to overwrite TranscriptTokens
// without also advancing TranscriptOffset, so a session resumed afterward
// (same session_id, transcript file still on disk and still growing) would
// have its next PostToolUse re-read from the stale, pre-SessionEnd offset —
// re-adding content SessionEnd's full read had already counted once.
//
// The bug only shows up when the transcript grows *between* the last
// incremental read and SessionEnd's full read — otherwise the stale offset
// happens to already sit at end-of-file and there's nothing for a resume to
// re-read. So this appends a line straight to disk (no PostToolUse call in
// between, modeling content that landed after the last incremental read but
// before the session ended) before firing SessionEnd, then simulates a
// resume (SessionStart on the same session_id) and one more PostToolUse
// with yet another line appended — and asserts the post-resume total is
// SessionEnd's total plus only that last line, never SessionEnd's total
// plus the pre-SessionEnd tail a second time on top of it.
func TestHandleSessionEndThenResumeDoesNotDoubleCountTranscriptUsage(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := writeTranscriptFixture(t, dir)

	live := bashInput(t, dir, "sess1", "echo one", bashOutput{Stdout: "one\n"})
	live.TranscriptPath = transcriptPath
	if _, err := handle(live); err != nil {
		t.Fatalf("live PostToolUse call errored: %v", err)
	}

	appendLine := func(tokens int) {
		t.Helper()
		f, err := os.OpenFile(transcriptPath, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("OpenFile for append errored: %v", err)
		}
		line := fmt.Sprintf(`{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":%d,"output_tokens":0}}}`, tokens) + "\n"
		if _, err := f.WriteString(line); err != nil {
			t.Fatalf("append WriteString errored: %v", err)
		}
		f.Close()
	}

	// Lands after the last incremental read but before SessionEnd — no
	// PostToolUse/Stop call sees it until SessionEnd's full reconciliation
	// read does.
	appendLine(30)

	se := sessionEndInput("sess1", dir)
	se.TranscriptPath = transcriptPath
	if _, err := handle(se); err != nil {
		t.Fatalf("SessionEnd call errored: %v", err)
	}
	afterEnd, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession after SessionEnd errored: %v", err)
	}
	if afterEnd.TranscriptTokens != 130 { // 100 from the fixture + the 30 appended above
		t.Fatalf("TranscriptTokens after SessionEnd = %d, want 130", afterEnd.TranscriptTokens)
	}

	// Simulate resuming the same session: SessionStart fires again for the
	// same session_id, then the transcript grows further and another
	// PostToolUse call lands.
	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("resume SessionStart call errored: %v", err)
	}
	appendLine(40)

	resumed := bashInput(t, dir, "sess1", "echo two", bashOutput{Stdout: "two\n"})
	resumed.TranscriptPath = transcriptPath
	if _, err := handle(resumed); err != nil {
		t.Fatalf("post-resume PostToolUse call errored: %v", err)
	}

	afterResume, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession after resume errored: %v", err)
	}
	if afterResume.TranscriptTokens != afterEnd.TranscriptTokens+40 {
		t.Fatalf("TranscriptTokens after resume = %d, want %d (SessionEnd's %d plus only the newly appended line's 40 tokens — a stale offset would double-count the pre-SessionEnd 30-token tail on top of this)",
			afterResume.TranscriptTokens, afterEnd.TranscriptTokens+40, afterEnd.TranscriptTokens)
	}
}

// TestHandleStopTracksTranscriptUsageForToolFreeSession is the regression
// test for the gap Stop closes: a session that never calls a tool (pure
// chat) used to have no stats file at all until SessionEnd — PostToolUse
// never fires to create one, so a crash or force-quit before SessionEnd
// meant the session's real token usage was silently excluded from every
// project/overall total. This asserts a bare Stop call, with no preceding
// PostToolUse, already persists nonzero transcript usage.
func TestHandleStopTracksTranscriptUsageForToolFreeSession(t *testing.T) {
	dir := t.TempDir()
	transcriptPath := writeTranscriptFixture(t, dir)

	in := hookInput{
		SessionID:      "sess1",
		Cwd:            dir,
		HookEventName:  "Stop",
		TranscriptPath: transcriptPath,
	}
	if _, err := handle(in); err != nil {
		t.Fatalf("Stop handling errored: %v", err)
	}

	sess, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if sess.TranscriptTokens == 0 || sess.TranscriptCostUSD == 0 || sess.TranscriptContentBytes == 0 {
		t.Fatalf("expected nonzero transcript usage after a bare Stop call (no tool calls this session), got %+v", sess)
	}
}

// TestHandlePostCompactSurvivesCrashBeforeSessionEnd is the end-to-end
// regression test for the second half of the same bug report Stop fixes:
// PostCompact used to wipe the whole session-stats file, relying on
// SessionEnd's later full transcript re-read to restore it — fine as long
// as SessionEnd actually fires, but a crash or force-quit between the
// compact and SessionEnd meant it never did, permanently losing the
// pre-compact usage. This drives a real turn (accruing transcript usage via
// Stop), fires PostCompact, and then simulates a crash by calling
// SumProject directly instead of ever calling SessionEnd — the pre-compact
// usage must still be there.
func TestHandlePostCompactSurvivesCrashBeforeSessionEnd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	transcriptPath := writeTranscriptFixture(t, dir)

	stopIn := hookInput{
		SessionID:      "sess1",
		Cwd:            dir,
		HookEventName:  "Stop",
		TranscriptPath: transcriptPath,
	}
	if _, err := handle(stopIn); err != nil {
		t.Fatalf("Stop handling errored: %v", err)
	}

	preCompact, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession before compact errored: %v", err)
	}
	if preCompact.TranscriptTokens == 0 {
		t.Fatalf("expected nonzero transcript usage before compact, got %+v", preCompact)
	}

	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "PostCompact"}); err != nil {
		t.Fatalf("PostCompact handling errored: %v", err)
	}

	// No further hook call — this is the simulated crash: SessionEnd never
	// fires, so nothing re-reads the transcript from scratch to restore
	// what PostCompact just touched.
	rollup, err := stats.SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject errored: %v", err)
	}
	if rollup.TranscriptTokens != preCompact.TranscriptTokens {
		t.Fatalf("pre-compact transcript usage lost after a simulated crash: rollup=%+v, want TranscriptTokens=%d",
			rollup, preCompact.TranscriptTokens)
	}
}

func TestHandleSessionEndReadsTranscriptUsage(t *testing.T) {
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	transcriptPath := writeTranscriptFixture(t, dir)
	in := sessionEndInput("sess1", dir)
	in.TranscriptPath = transcriptPath

	if _, err := handle(in); err != nil {
		t.Fatalf("handle errored: %v", err)
	}

	rollup, err := stats.SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject errored: %v", err)
	}
	if rollup.TranscriptTokens == 0 {
		t.Fatalf("expected the rollup's TranscriptTokens to reflect the transcript, got %+v", rollup)
	}
	if rollup.TranscriptCostUSD == 0 {
		t.Fatalf("expected the rollup's TranscriptCostUSD to be nonzero, got %+v", rollup)
	}

	// Regression: the per-session file must carry the transcript usage too
	// — GetSessionStats (agent-winglet-app) reads this file back later for
	// the per-session breakdown, and SumProject itself only sees what's on
	// disk.
	sess, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if sess.TranscriptContentBytes == 0 {
		t.Fatalf("expected the per-session file's TranscriptContentBytes to be persisted, got %+v", sess)
	}
}

// TestHandleSessionEndPersistsTranscriptOnlySession covers the session shape
// that used to fall through the cracks entirely: no Bash calls at all (a
// Read-only/Edit-only session), so dedup/budget/retire never fire and
// IsZero() stays true throughout — but PostToolUse still tracked real
// transcript usage live (see recordTranscriptDelta, unconditional on tool
// name). Before this fix, IsZero() gated the entire SessionEnd persist path,
// so this session's real transcript data was silently dropped: never
// written back to disk, and — most visibly — would have kept getting summed
// as "still live" by the desktop app's Overview/Projects rollup forever,
// even after the session was long gone. It should now persist to disk (so
// it counts toward every future stats.SumProject call) while still emitting
// no receipt (a suppression-activity report with nothing to report stays
// silent).
func TestHandleSessionEndPersistsTranscriptOnlySession(t *testing.T) {
	dir := t.TempDir()
	readIn := hookInput{
		SessionID:      "sess1",
		Cwd:            dir,
		HookEventName:  "PostToolUse",
		ToolName:       "Read",
		ToolInput:      json.RawMessage(`{"file_path":"/tmp/f"}`),
		ToolResponse:   json.RawMessage(`{"type":"text","file":{"content":"x"}}`),
		TranscriptPath: writeTranscriptFixture(t, dir),
	}
	if _, err := handle(readIn); err != nil {
		t.Fatalf("Read call errored: %v", err)
	}

	sessBeforeEnd, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if !sessBeforeEnd.IsZero() {
		t.Fatalf("expected IsZero()=true (no Bash dedup/trim/retire fired), got %+v", sessBeforeEnd)
	}
	if sessBeforeEnd.TranscriptContentBytes == 0 {
		t.Fatalf("expected live transcript tracking to have recorded real usage from the Read call, got %+v", sessBeforeEnd)
	}

	in := sessionEndInput("sess1", dir)
	in.TranscriptPath = readIn.TranscriptPath
	out, err := handle(in)
	if err != nil {
		t.Fatalf("SessionEnd handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("a transcript-only session (no suppression activity) should still emit no receipt, got %+v", out)
	}

	sess, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession after SessionEnd errored: %v", err)
	}
	if sess.TranscriptContentBytes == 0 {
		t.Fatalf("expected the transcript-only session's usage to still be on disk after SessionEnd, got %+v", sess)
	}

	rollup, err := stats.SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject errored: %v", err)
	}
	if rollup.Sessions != 1 {
		t.Fatalf("Sessions = %d, want 1 (a transcript-only session should still count toward the rollup)", rollup.Sessions)
	}
	if rollup.TranscriptContentBytes == 0 {
		t.Fatalf("expected the transcript-only session's usage to show up in the rollup, got %+v", rollup)
	}
}

func TestHandleSessionEndUnreadableTranscriptStillEmitsReceipt(t *testing.T) {
	t.Setenv(quietEnvVar, "0")
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	in := sessionEndInput("sess1", dir)
	in.TranscriptPath = "/does/not/exist.jsonl"

	out, err := handle(in)
	if err != nil {
		t.Fatalf("handle errored with an unreadable transcript path: %v", err)
	}
	if out == nil || out.SystemMessage == "" {
		t.Fatalf("expected a receipt even when the transcript can't be read, got %+v", out)
	}

	rollup, err := stats.SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject errored: %v", err)
	}
	if rollup.TranscriptTokens != 0 {
		t.Fatalf("expected TranscriptTokens to stay zero for an unreadable transcript, got %d", rollup.TranscriptTokens)
	}
}

func writeTranscriptFixture(t *testing.T, dir string) string {
	t.Helper()
	path := dir + "/fixture-transcript.jsonl"
	assistantLine := `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":50}}}` + "\n"
	userLine := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"hi"}]}}` + "\n"
	line := assistantLine + userLine
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript fixture: %v", err)
	}
	return path
}

func TestHandleSessionEndRespectsQuietEnvVar(t *testing.T) {
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	t.Setenv(quietEnvVar, "1")

	out, err := handle(sessionEndInput("sess1", dir))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("AGENT_WINGLET_QUIET should suppress the receipt message, got %+v", out)
	}
}

func TestHandleSessionEndStillPersistsSessionWhenQuiet(t *testing.T) {
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	t.Setenv(quietEnvVar, "1")
	if _, err := handle(sessionEndInput("sess1", dir)); err != nil {
		t.Fatalf("handle errored: %v", err)
	}

	rollup, err := stats.SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject errored: %v", err)
	}
	if rollup.Sessions != 1 || rollup.DedupHits != 1 {
		t.Fatalf("session not persisted while quiet: %+v", rollup)
	}
}

func TestHandleSessionEndAccumulatesLifetimeAcrossSessions(t *testing.T) {
	t.Setenv(quietEnvVar, "0")
	dir := t.TempDir()

	sess1 := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(sess1); err != nil {
		t.Fatalf("sess1 first call errored: %v", err)
	}
	if _, err := handle(sess1); err != nil {
		t.Fatalf("sess1 repeat call errored: %v", err)
	}
	if _, err := handle(sessionEndInput("sess1", dir)); err != nil {
		t.Fatalf("sess1 SessionEnd errored: %v", err)
	}

	sess2 := bashInput(t, dir, "sess2", "echo bye", bashOutput{Stdout: "bye\n"})
	if _, err := handle(sess2); err != nil {
		t.Fatalf("sess2 first call errored: %v", err)
	}
	if _, err := handle(sess2); err != nil {
		t.Fatalf("sess2 repeat call errored: %v", err)
	}
	out, err := handle(sessionEndInput("sess2", dir))
	if err != nil {
		t.Fatalf("sess2 SessionEnd errored: %v", err)
	}
	if out == nil || !strings.Contains(out.SystemMessage, "Lifetime across 2 sessions") {
		t.Fatalf("expected lifetime tally across 2 sessions, got %+v", out)
	}
}

func TestHandleSessionStartInvalidatesStatsTally(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	if _, err := handle(hookInput{SessionID: "sess1", Cwd: dir, HookEventName: "SessionStart"}); err != nil {
		t.Fatalf("SessionStart handling errored: %v", err)
	}

	out, err := handle(sessionEndInput("sess1", dir))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("stats tally should be wiped by SessionStart, got receipt %+v", out)
	}
}

func TestHandleSessionEndRespectsQuietConfigFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	if err := config.Save(&config.Config{Quiet: true}); err != nil {
		t.Fatalf("config.Save errored: %v", err)
	}

	out, err := handle(sessionEndInput("sess1", dir))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out != nil {
		t.Fatalf("config-file Quiet=true should suppress the receipt message, got %+v", out)
	}
}

func TestHandleSessionEndConfigFileCanOptOutOfDefaultQuiet(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	if err := config.Save(&config.Config{Quiet: false}); err != nil {
		t.Fatalf("config.Save errored: %v", err)
	}

	out, err := handle(sessionEndInput("sess1", dir))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil || out.SystemMessage == "" {
		t.Fatalf("config-file Quiet=false should opt back into the receipt despite the default, got %+v", out)
	}
}

func TestHandleSessionEndQuietEnvVarOverridesConfigFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	repeatIn := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(repeatIn); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	if err := config.Save(&config.Config{Quiet: true}); err != nil {
		t.Fatalf("config.Save errored: %v", err)
	}
	t.Setenv(quietEnvVar, "0")

	out, err := handle(sessionEndInput("sess1", dir))
	if err != nil {
		t.Fatalf("handle errored: %v", err)
	}
	if out == nil || out.SystemMessage == "" {
		t.Fatalf("AGENT_WINGLET_QUIET=0 should override a quiet config file, got %+v", out)
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

func TestPassthroughBashCallRecordsNoActivity(t *testing.T) {
	dir := t.TempDir()

	// Short, non-repeat, passes through untouched — nothing suppressed, so
	// nothing should be recorded at all.
	in := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(in); err != nil {
		t.Fatalf("handle errored: %v", err)
	}

	s, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if !s.IsZero() {
		t.Fatalf("a passthrough-only session has no mechanism activity and should report IsZero, got %+v", s)
	}
}

func TestBudgetBytesOmittedOnTrim(t *testing.T) {
	dir := t.TempDir()
	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})

	if _, err := handle(in); err != nil {
		t.Fatalf("handle errored: %v", err)
	}

	s, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.BudgetBytesOmitted <= 0 || s.BudgetBytesOmitted >= int64(len(longOutput)) {
		t.Fatalf("BudgetBytesOmitted = %d, want a positive value less than the original output length (%d)",
			s.BudgetBytesOmitted, len(longOutput))
	}
}

func TestDedupBytesOnDedupHit(t *testing.T) {
	dir := t.TempDir()
	in := bashInput(t, dir, "sess1", "echo hi", bashOutput{Stdout: "hi\n"})
	if _, err := handle(in); err != nil {
		t.Fatalf("first call errored: %v", err)
	}
	if _, err := handle(in); err != nil {
		t.Fatalf("repeat call errored: %v", err)
	}

	s, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.DedupBytes != int64(len("hi\n")) {
		t.Fatalf("DedupBytes = %d, want %d", s.DedupBytes, len("hi\n"))
	}
}

func TestRetiredBytesOnRetiredCall(t *testing.T) {
	dir := t.TempDir()
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}
	response := json.RawMessage(`{"matches":["a"]}`)
	if _, err := handle(investigateInput("sess1", dir, "Grep", json.RawMessage(`{"pattern":"a"}`), response)); err != nil {
		t.Fatalf("retiring call errored: %v", err)
	}

	s, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.RetiredBytes != int64(len(response)) {
		t.Fatalf("RetiredBytes = %d, want %d", s.RetiredBytes, len(response))
	}
}

// TestRetiredBytesOnCallThresholdRetiredCall is
// TestRetiredBytesOnRetiredCall's counterpart for the pre-boundary
// investigate-call-threshold retire path (spec-nextsteps.md's §4) — it
// shares RecordRetire/RetiredCalls/RetiredBytes with the post-boundary path,
// so this guards that the threshold call site records against the same
// counters, with the boundary never crossed.
func TestRetiredBytesOnCallThresholdRetiredCall(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < investigateCallThreshold; i++ {
		if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
			t.Fatalf("seeding call %d errored: %v", i+1, err)
		}
	}
	response := json.RawMessage(`{"matches":["a"]}`)
	if _, err := handle(investigateInput("sess1", dir, "Grep", json.RawMessage(`{"pattern":"a"}`), response)); err != nil {
		t.Fatalf("retiring call errored: %v", err)
	}

	s, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.RetiredBytes != int64(len(response)) {
		t.Fatalf("RetiredBytes = %d, want %d", s.RetiredBytes, len(response))
	}
	if s.RetiredCalls != 1 {
		t.Fatalf("RetiredCalls = %d, want 1", s.RetiredCalls)
	}
}

// TestRetiredBytesOnBashRetiredCall is TestRetiredBytesOnRetiredCall's
// counterpart for the new post-boundary Bash retire path — it shares
// RecordRetire/RetiredCalls/RetiredBytes with handleRetireInvestigate, so
// this guards that Bash's own call site records against the same counters.
func TestRetiredBytesOnBashRetiredCall(t *testing.T) {
	dir := t.TempDir()
	if _, err := handle(toolCallInput("sess1", dir, "Read")); err != nil {
		t.Fatalf("seeding investigate call errored: %v", err)
	}
	if _, err := handle(toolCallInput("sess1", dir, "Edit")); err != nil {
		t.Fatalf("crossing call errored: %v", err)
	}
	longOutput := linesOfApproxTokens(budgetTokenThreshold + 1)
	in := bashInput(t, dir, "sess1", "go build ./...", bashOutput{Stdout: longOutput})
	if _, err := handle(in); err != nil {
		t.Fatalf("retiring call errored: %v", err)
	}

	s, err := stats.LoadSession(dir, "sess1")
	if err != nil {
		t.Fatalf("LoadSession errored: %v", err)
	}
	if s.RetiredBytes != int64(len(longOutput)) {
		t.Fatalf("RetiredBytes = %d, want %d", s.RetiredBytes, len(longOutput))
	}
	if s.RetiredCalls != 1 {
		t.Fatalf("RetiredCalls = %d, want 1", s.RetiredCalls)
	}
}

// TestMigrateLegacyDataFoldsCurrentLayoutLifetimeIntoSeedSession covers the
// migration path every existing install hits: a lifetime.stats.json already
// sitting in root's own state dir from before project/overall totals became
// a pure sum of session files (see internal/stats.SumProject). Its counts
// must survive the upgrade by landing on the legacy-migrated seed session,
// and the now-orphaned file must be removed so it doesn't confuse a future
// reader.
func TestMigrateLegacyDataFoldsCurrentLayoutLifetimeIntoSeedSession(t *testing.T) {
	dir := t.TempDir()
	d, err := statedir.Dir(dir)
	if err != nil {
		t.Fatalf("statedir.Dir errored: %v", err)
	}
	lifetimePath := filepath.Join(d, stats.LifetimeFileName)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	if err := os.WriteFile(lifetimePath, []byte(`{"sessions":3,"dedupHits":5,"dedupBytes":500}`), 0o644); err != nil {
		t.Fatalf("writing lifetime.stats.json errored: %v", err)
	}

	migrateLegacyData(dir, dir)

	if _, err := os.Stat(lifetimePath); !os.IsNotExist(err) {
		t.Fatalf("expected the orphaned lifetime.stats.json to be removed, stat err = %v", err)
	}

	seed, err := stats.LoadSession(dir, legacySessionID)
	if err != nil {
		t.Fatalf("LoadSession for seed errored: %v", err)
	}
	if seed.DedupHits != 5 || seed.DedupBytes != 500 {
		t.Fatalf("seed session = %+v, want DedupHits=5 DedupBytes=500", seed)
	}

	rollup, err := stats.SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject errored: %v", err)
	}
	if rollup.DedupHits != 5 || rollup.DedupBytes != 500 {
		t.Fatalf("rollup after migration = %+v, want DedupHits=5 DedupBytes=500", rollup)
	}
}

// TestMigrateLegacyDataMergesRepeatedCallsWithoutDoubleCounting guards the
// order-independence property the old Lifetime-file merge relied on: calling
// migrateLegacyData again (as every SessionStart/PostCompact does) with no
// new legacy file present must leave the seed session untouched, not fold
// the same numbers in twice.
func TestMigrateLegacyDataMergesRepeatedCallsWithoutDoubleCounting(t *testing.T) {
	dir := t.TempDir()
	d, err := statedir.Dir(dir)
	if err != nil {
		t.Fatalf("statedir.Dir errored: %v", err)
	}
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatalf("MkdirAll errored: %v", err)
	}
	lifetimePath := filepath.Join(d, stats.LifetimeFileName)
	if err := os.WriteFile(lifetimePath, []byte(`{"sessions":1,"retiredCalls":2,"retiredBytes":20}`), 0o644); err != nil {
		t.Fatalf("writing lifetime.stats.json errored: %v", err)
	}

	migrateLegacyData(dir, dir)
	migrateLegacyData(dir, dir) // second call: file is already gone, must no-op

	rollup, err := stats.SumProject(dir)
	if err != nil {
		t.Fatalf("SumProject errored: %v", err)
	}
	if rollup.RetiredCalls != 2 || rollup.RetiredBytes != 20 {
		t.Fatalf("rollup after two migration calls = %+v, want RetiredCalls=2 RetiredBytes=20 (not doubled)", rollup)
	}
}
