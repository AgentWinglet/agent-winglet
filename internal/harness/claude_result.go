package harness

import "encoding/json"

// ClaudeResult mirrors the subset of the top-level object that
// `claude -p --output-format json` prints to stdout on completion. The CLI
// emits additional fields (result text, stop_reason, permission_denials,
// ...); json.Unmarshal silently ignores what this struct doesn't name.
//
// total_cost_usd is used as the usage_per_solve numerator. It's Anthropic's
// own computed cost from real token usage and model pricing — the closest
// proxy to actual weekly-cap consumption available without a scriptable way
// to read account-level usage. It is still an approximation: see
// harness/README.md for why the measurement gate isn't considered closed on
// this metric alone.
type ClaudeResult struct {
	SessionID    string  `json:"session_id"`
	NumTurns     int     `json:"num_turns"`
	TotalCostUSD float64 `json:"total_cost_usd"`
	DurationMS   int     `json:"duration_ms"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	} `json:"usage"`
}

// ParseClaudeResult parses the JSON object `claude -p --output-format json`
// prints to stdout on completion.
func ParseClaudeResult(data []byte) (ClaudeResult, error) {
	var r ClaudeResult
	err := json.Unmarshal(data, &r)
	return r, err
}
