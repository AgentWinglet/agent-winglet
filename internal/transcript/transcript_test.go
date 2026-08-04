package transcript

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/umitkaanusta/agent-winglet/internal/pricing"
)

func writeFixture(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	var data []byte
	for _, l := range lines {
		data = append(data, []byte(l)...)
		data = append(data, '\n')
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}
	return path
}

func TestReadSessionUsageSumsTokensBytesAndCost(t *testing.T) {
	assistantLine := `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{` +
		`"input_tokens":100,"output_tokens":50,` +
		`"cache_read_input_tokens":200,"cache_creation_input_tokens":300,` +
		`"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}}`
	userLine := `{"type":"user","message":{"role":"user","content":[{"tool_use_id":"x","type":"tool_result","content":"hello world"}]}}`

	path := writeFixture(t, []string{assistantLine, userLine})

	u, err := ReadSessionUsage(path)
	if err != nil {
		t.Fatalf("ReadSessionUsage errored: %v", err)
	}

	// cache_read_input_tokens (200) and output_tokens (50) are deliberately
	// excluded: they don't represent content newly fed to the model on this
	// line, so including them would inflate Tokens/CostUSD relative to
	// ContentBytes (which counts each line's content once) — see
	// SessionUsage's doc comment.
	wantTokens := int64(100 + 100 + 200) // input + 5m + 1h
	if u.Tokens != wantTokens {
		t.Fatalf("Tokens = %d, want %d", u.Tokens, wantTokens)
	}

	rate := pricing.Lookup("claude-sonnet-5")
	wantCost := float64(100)*rate.Input/1e6 +
		float64(100)*rate.CacheWrite5m/1e6 + float64(200)*rate.CacheWrite1h/1e6
	if u.CostUSD != wantCost {
		t.Fatalf("CostUSD = %v, want %v", u.CostUSD, wantCost)
	}

	wantBytes := int64(len(`[{"tool_use_id":"x","type":"tool_result","content":"hello world"}]`))
	if u.ContentBytes != wantBytes {
		t.Fatalf("ContentBytes = %d, want %d", u.ContentBytes, wantBytes)
	}
}

func TestReadSessionUsageMissingFileIsZeroNoError(t *testing.T) {
	u, err := ReadSessionUsage(filepath.Join(t.TempDir(), "does-not-exist.jsonl"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if u != (SessionUsage{}) {
		t.Fatalf("expected zero SessionUsage, got %+v", u)
	}
}

func TestReadSessionUsageEmptyPathIsZeroNoError(t *testing.T) {
	u, err := ReadSessionUsage("")
	if err != nil {
		t.Fatalf("expected no error for empty path, got %v", err)
	}
	if u != (SessionUsage{}) {
		t.Fatalf("expected zero SessionUsage, got %+v", u)
	}
}

func TestReadSessionUsageSkipsMalformedLines(t *testing.T) {
	good := `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":10,"output_tokens":5}}}`
	path := writeFixture(t, []string{"{not valid json", good, "also not json"})

	u, err := ReadSessionUsage(path)
	if err != nil {
		t.Fatalf("expected no error for malformed lines, got %v", err)
	}
	if u.Tokens != 10 {
		t.Fatalf("Tokens = %d, want 10 (only the well-formed line should count)", u.Tokens)
	}
}

// TestReadSessionUsageManyTurnsDontInflateTokensOrCost is a regression test
// for the exact bug report this fix addresses: a long session where every
// turn re-reads the whole growing context from cache (cache_read_input_
// tokens) must not make Tokens/CostUSD scale with turn count. Ten turns each
// replaying a 100K-token cache (a realistic long-session shape) would have
// summed to 1M+ cache-read tokens under the old accounting, for a session
// whose actual new content is a handful of small tool_result lines — pricing
// that against a tiny ContentBytes total is what produced a $18+ estimate
// for 3.5 MiB of suppressed content.
func TestReadSessionUsageManyTurnsDontInflateTokensOrCost(t *testing.T) {
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{`+
			`"input_tokens":10,"output_tokens":20,"cache_read_input_tokens":100000}}}`)
		lines = append(lines, `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}`)
	}
	path := writeFixture(t, lines)

	u, err := ReadSessionUsage(path)
	if err != nil {
		t.Fatalf("ReadSessionUsage errored: %v", err)
	}

	if wantTokens := int64(10 * 10); u.Tokens != wantTokens {
		t.Fatalf("Tokens = %d, want %d (cache-read replays across 10 turns must not accumulate)", u.Tokens, wantTokens)
	}

	rate := pricing.Lookup("claude-sonnet-5")
	wantCost := float64(10*10) * rate.Input / 1e6
	if u.CostUSD != wantCost {
		t.Fatalf("CostUSD = %v, want %v (cache-read and output cost must not accumulate into the content price)",
			u.CostUSD, wantCost)
	}

	costPerByte := u.CostUSD / float64(u.ContentBytes)
	if costPerByte > 0.01 {
		t.Fatalf("costPerByte = %v, implausibly high for plain text content (want a small fraction of a cent per byte)",
			costPerByte)
	}
}

// TestReadSessionUsageFromAccumulatesAcrossCalls is the shape PostToolUse
// actually uses it in: read from offset 0 (nothing yet), some lines get
// appended (simulating the transcript growing between tool calls), read
// again from the returned offset. The second call's delta must cover only
// the newly appended lines, not the whole file again — that's the entire
// point of tracking offset instead of calling ReadSessionUsage repeatedly.
func TestReadSessionUsageFromAccumulatesAcrossCalls(t *testing.T) {
	line1 := `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":10,"output_tokens":0}}}`
	path := writeFixture(t, []string{line1})

	u1, offset1, err := ReadSessionUsageFrom(path, 0)
	if err != nil {
		t.Fatalf("first ReadSessionUsageFrom errored: %v", err)
	}
	if u1.Tokens != 10 {
		t.Fatalf("first delta Tokens = %d, want 10", u1.Tokens)
	}
	if offset1 == 0 {
		t.Fatalf("offset1 should advance past the first line, got 0")
	}

	// Nothing new since offset1: delta should be zero and offset unchanged.
	u2, offset2, err := ReadSessionUsageFrom(path, offset1)
	if err != nil {
		t.Fatalf("second ReadSessionUsageFrom (no new data) errored: %v", err)
	}
	if u2 != (SessionUsage{}) {
		t.Fatalf("expected a zero delta with nothing new appended, got %+v", u2)
	}
	if offset2 != offset1 {
		t.Fatalf("offset should not move when nothing new was read: offset1=%d offset2=%d", offset1, offset2)
	}

	// Append a second line (simulating the transcript growing between tool
	// calls) and read again from offset1 — the delta must cover only the
	// new line.
	line2 := `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":25,"output_tokens":0}}}`
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile for append errored: %v", err)
	}
	if _, err := f.WriteString(line2 + "\n"); err != nil {
		t.Fatalf("append WriteString errored: %v", err)
	}
	f.Close()

	u3, offset3, err := ReadSessionUsageFrom(path, offset1)
	if err != nil {
		t.Fatalf("third ReadSessionUsageFrom errored: %v", err)
	}
	if u3.Tokens != 25 {
		t.Fatalf("third delta Tokens = %d, want 25 (only the newly appended line)", u3.Tokens)
	}
	if offset3 <= offset1 {
		t.Fatalf("offset3 (%d) should advance past offset1 (%d)", offset3, offset1)
	}
}

// TestReadSessionUsageFromDoesNotConsumePartialFinalLine is the concurrency
// case this function exists to handle safely: Claude Code can still be
// mid-write on the transcript's last line when a hook runs. A naive
// line-splitter would either error on invalid JSON (fine, fail-soft) but
// then advance the offset past the torn bytes anyway, permanently losing
// that line's usage once the writer finishes it — this must not happen.
func TestReadSessionUsageFromDoesNotConsumePartialFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	complete := `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":10,"output_tokens":0}}}` + "\n"
	partial := `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tok` // no trailing newline: write in progress
	if err := os.WriteFile(path, []byte(complete+partial), 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}

	u, offset, err := ReadSessionUsageFrom(path, 0)
	if err != nil {
		t.Fatalf("ReadSessionUsageFrom errored: %v", err)
	}
	if u.Tokens != 10 {
		t.Fatalf("Tokens = %d, want 10 (only the complete line should count)", u.Tokens)
	}
	if want := int64(len(complete)); offset != want {
		t.Fatalf("offset = %d, want %d (must stop exactly at the end of the last complete line, not consume the partial one)",
			offset, want)
	}

	// Simulate the writer finishing that line later: append the rest plus a
	// newline, then read again from the offset returned above. The
	// previously-partial line must now be read whole, not skipped.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile for append errored: %v", err)
	}
	if _, err := f.WriteString(`ens":15,"output_tokens":0}}}` + "\n"); err != nil {
		t.Fatalf("append WriteString errored: %v", err)
	}
	f.Close()

	u2, _, err := ReadSessionUsageFrom(path, offset)
	if err != nil {
		t.Fatalf("follow-up ReadSessionUsageFrom errored: %v", err)
	}
	if u2.Tokens != 15 {
		t.Fatalf("Tokens = %d, want 15 (the completed line should be read whole once finished, not lost)", u2.Tokens)
	}
}

func TestReadSessionUsageMixedModelsUseEachLinesOwnRate(t *testing.T) {
	opus := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":100,"output_tokens":0}}}`
	sonnet := `{"type":"assistant","message":{"model":"claude-sonnet-5","usage":{"input_tokens":100,"output_tokens":0}}}`
	path := writeFixture(t, []string{opus, sonnet})

	u, err := ReadSessionUsage(path)
	if err != nil {
		t.Fatalf("ReadSessionUsage errored: %v", err)
	}

	opusRate := pricing.Lookup("claude-opus-5")
	sonnetRate := pricing.Lookup("claude-sonnet-5")
	want := float64(100)*opusRate.Input/1e6 + float64(100)*sonnetRate.Input/1e6
	if u.CostUSD != want {
		t.Fatalf("CostUSD = %v, want %v (each line priced at its own model's rate)", u.CostUSD, want)
	}
}
