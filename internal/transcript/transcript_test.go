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

	wantTokens := int64(100 + 200 + 100 + 200) // input + cache_read + 5m + 1h
	if u.Tokens != wantTokens {
		t.Fatalf("Tokens = %d, want %d", u.Tokens, wantTokens)
	}

	rate := pricing.Lookup("claude-sonnet-5")
	wantCost := float64(100)*rate.Input/1e6 + float64(50)*rate.Output/1e6 +
		float64(200)*rate.CacheRead/1e6 + float64(100)*rate.CacheWrite5m/1e6 + float64(200)*rate.CacheWrite1h/1e6
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
