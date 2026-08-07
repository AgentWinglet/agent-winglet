package codexrollout

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/umitkaanusta/agent-winglet/internal/pricing"
	"github.com/umitkaanusta/agent-winglet/internal/transcript"
)

func writeRolloutFixture(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
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

func TestReadSessionUsageUsesLatestCumulativeTokenCount(t *testing.T) {
	path := filepath.Join("testdata", "redacted_rollout.jsonl")

	u, err := ReadSessionUsage(path)
	if err != nil {
		t.Fatalf("ReadSessionUsage errored: %v", err)
	}

	// Latest cumulative event: input_tokens=1200, cached_input_tokens=500
	// (a subset of input_tokens, not additional — see priceTokenUsage),
	// cache_write_input_tokens=30. Fresh (non-cached) input is 1200-500=700,
	// so Tokens = 700+30 = 730.
	wantTokens := int64(700 + 30)
	if u.Tokens != wantTokens {
		t.Fatalf("Tokens = %d, want %d", u.Tokens, wantTokens)
	}

	rate := pricing.LookupOpenAI("gpt-5-codex")
	wantCost := float64(700)*rate.Input/1e6 + float64(30)*rate.CacheWrite5m/1e6
	if u.CostUSD != wantCost {
		t.Fatalf("CostUSD = %v, want %v", u.CostUSD, wantCost)
	}

	wantBytes := int64(len("Please inspect the project and summarize the next step.") +
		len("Command: rg --files\nOutput:\nSPEC.md\ninternal/transcript/transcript.go\n"))
	if u.ContentBytes != wantBytes {
		t.Fatalf("ContentBytes = %d, want %d", u.ContentBytes, wantBytes)
	}
}

func TestReadSessionUsageWithoutTokenCountFallsBackToContentBytesOnly(t *testing.T) {
	path := writeRolloutFixture(t, []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}],"model":"gpt-5-codex"}}`,
		`{"type":"response_item","payload":{"type":"function_call_output","output":"world"}}`,
	})

	u, err := ReadSessionUsage(path)
	if err != nil {
		t.Fatalf("ReadSessionUsage errored: %v", err)
	}
	if u.Tokens != 0 || u.CostUSD != 0 {
		t.Fatalf("expected no token/cost usage without token_count rows, got %+v", u)
	}
	if u.ContentBytes != int64(len("hello")+len("world")) {
		t.Fatalf("ContentBytes = %d, want %d", u.ContentBytes, len("hello")+len("world"))
	}
}

func TestReadSessionUsageMissingUnreadableAndMalformedAreFailSoft(t *testing.T) {
	u, err := ReadSessionUsage(filepath.Join(t.TempDir(), "missing.jsonl"))
	if err != nil {
		t.Fatalf("missing file returned error: %v", err)
	}
	if u != (transcript.SessionUsage{}) {
		t.Fatalf("missing file usage = %+v, want zero", u)
	}

	path := writeRolloutFixture(t, []string{
		`{not json`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"ok","model":"gpt-5-codex"}}`,
		`also not json`,
	})
	u, err = ReadSessionUsage(path)
	if err != nil {
		t.Fatalf("malformed file returned error: %v", err)
	}
	if u.ContentBytes != int64(len("ok")) {
		t.Fatalf("ContentBytes = %d, want %d", u.ContentBytes, len("ok"))
	}
}

func TestReadSessionUsageFromReturnsDeltaForCumulativeTokens(t *testing.T) {
	firstToken := `{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":90,"cache_write_input_tokens":10,"output_tokens":5,"reasoning_output_tokens":2,"total_tokens":115}}}}`
	path := writeRolloutFixture(t, []string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"first","model":"gpt-5-codex"}}`,
		firstToken,
	})

	first, offset, err := ReadSessionUsageFrom(path, 0, transcript.SessionUsage{})
	if err != nil {
		t.Fatalf("first ReadSessionUsageFrom errored: %v", err)
	}
	// input_tokens=100, cached_input_tokens=90 (subset of input_tokens),
	// cache_write_input_tokens=10 -> fresh input = 100-90=10, Tokens = 10+10=20.
	if first.Tokens != 20 || first.ContentBytes != int64(len("first")) {
		t.Fatalf("first delta = %+v, want Tokens=20 ContentBytes=%d", first, len("first"))
	}

	appendLines(t, path,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"second","model":"gpt-5-codex"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":180,"cached_input_tokens":120,"cache_write_input_tokens":20,"output_tokens":9,"reasoning_output_tokens":3,"total_tokens":209}}}}`,
	)

	second, newOffset, err := ReadSessionUsageFrom(path, offset, first)
	if err != nil {
		t.Fatalf("second ReadSessionUsageFrom errored: %v", err)
	}
	// Latest cumulative: fresh input = 180-120=60, Tokens = 60+20=80.
	// Delta against previous (20) = 60.
	if second.Tokens != 60 {
		t.Fatalf("second Tokens = %d, want 60 (latest cumulative 80 minus previous 20)", second.Tokens)
	}
	if second.ContentBytes != int64(len("second")) {
		t.Fatalf("second ContentBytes = %d, want %d", second.ContentBytes, len("second"))
	}
	if newOffset <= offset {
		t.Fatalf("newOffset = %d, want greater than %d", newOffset, offset)
	}
}

func TestReadSessionUsageFromDoesNotConsumePartialFinalLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rollout.jsonl")
	complete := `{"type":"response_item","payload":{"type":"message","role":"user","content":"done","model":"gpt-5-codex"}}` + "\n"
	partial := `{"type":"response_item","payload":{"type":"function_call_output","output":"later`
	if err := os.WriteFile(path, []byte(complete+partial), 0o644); err != nil {
		t.Fatalf("WriteFile errored: %v", err)
	}

	u, offset, err := ReadSessionUsageFrom(path, 0, transcript.SessionUsage{})
	if err != nil {
		t.Fatalf("ReadSessionUsageFrom errored: %v", err)
	}
	if u.ContentBytes != int64(len("done")) {
		t.Fatalf("ContentBytes = %d, want %d", u.ContentBytes, len("done"))
	}
	if offset != int64(len(complete)) {
		t.Fatalf("offset = %d, want %d", offset, len(complete))
	}

	appendRaw(t, path, ` output"}}`+"\n")
	u, _, err = ReadSessionUsageFrom(path, offset, transcript.SessionUsage{})
	if err != nil {
		t.Fatalf("follow-up ReadSessionUsageFrom errored: %v", err)
	}
	if u.ContentBytes != int64(len("later output")) {
		t.Fatalf("ContentBytes = %d, want %d", u.ContentBytes, len("later output"))
	}
}

func appendLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	var data []byte
	for _, l := range lines {
		data = append(data, []byte(l)...)
		data = append(data, '\n')
	}
	appendRaw(t, path, string(data))
}

func appendRaw(t *testing.T, path, data string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("OpenFile for append errored: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(data); err != nil {
		t.Fatalf("append WriteString errored: %v", err)
	}
}
