package harness

import "testing"

// Captured from a real `claude -p ... --output-format json` run — only the
// fields ClaudeResult names are asserted; everything else in the payload is
// expected to be ignored rather than erroring.
const sampleClaudeJSON = `{"is_error":false,"duration_api_ms":2224,"num_turns":1,"stop_reason":"end_turn","session_id":"ee68d0af-c484-4600-832a-beb0ea55eb44","total_cost_usd":0.1004097,"usage":{"input_tokens":2,"cache_creation_input_tokens":15722,"cache_read_input_tokens":19589,"output_tokens":13,"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard"},"result":"Hi! What are we working on today?","type":"result","duration_ms":2373,"uuid":"254ca1c6-5504-4bb2-98e1-983c5a948403"}`

func TestParseClaudeResult(t *testing.T) {
	r, err := ParseClaudeResult([]byte(sampleClaudeJSON))
	if err != nil {
		t.Fatalf("ParseClaudeResult: %v", err)
	}
	if r.SessionID != "ee68d0af-c484-4600-832a-beb0ea55eb44" {
		t.Errorf("SessionID = %q, want ee68d0af-...", r.SessionID)
	}
	if r.NumTurns != 1 {
		t.Errorf("NumTurns = %d, want 1", r.NumTurns)
	}
	if r.TotalCostUSD != 0.1004097 {
		t.Errorf("TotalCostUSD = %v, want 0.1004097", r.TotalCostUSD)
	}
	if r.DurationMS != 2373 {
		t.Errorf("DurationMS = %d, want 2373", r.DurationMS)
	}
	if r.Usage.InputTokens != 2 || r.Usage.OutputTokens != 13 ||
		r.Usage.CacheReadInputTokens != 19589 || r.Usage.CacheCreationInputTokens != 15722 {
		t.Errorf("Usage = %+v, unexpected", r.Usage)
	}
}

func TestParseClaudeResult_Malformed(t *testing.T) {
	if _, err := ParseClaudeResult([]byte("not json")); err == nil {
		t.Fatal("ParseClaudeResult on malformed input: got nil error, want an error")
	}
}

func TestNewRecord(t *testing.T) {
	cr, err := ParseClaudeResult([]byte(sampleClaudeJSON))
	if err != nil {
		t.Fatalf("ParseClaudeResult: %v", err)
	}
	rec := NewRecord("fix-typo", "hook", true, cr)
	if rec.Task != "fix-typo" || rec.Variant != "hook" || !rec.Success {
		t.Errorf("NewRecord identity fields = %+v, unexpected", rec)
	}
	if rec.SessionID != cr.SessionID || rec.NumTurns != cr.NumTurns || rec.TotalCostUSD != cr.TotalCostUSD {
		t.Errorf("NewRecord did not carry over ClaudeResult fields: %+v", rec)
	}
	if rec.Timestamp.IsZero() {
		t.Error("NewRecord Timestamp is zero, want set to time of call")
	}
}
