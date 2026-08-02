// Package harness implements the paired-run measurement gate required by
// agent-winglet-v1-spec.md §5 before any context-saving lever counts as
// validated: same task, run with and without the hook, scored on
//
//	usage_per_solve = (usage consumed) / (tasks completed successfully)
package harness

import "time"

// Record is one paired-run trial: a single `claude -p` invocation against
// a task, in either the "hook" or "control" variant.
type Record struct {
	Timestamp                time.Time `json:"timestamp"`
	Task                     string    `json:"task"`
	Variant                  string    `json:"variant"`
	SessionID                string    `json:"session_id"`
	Success                  bool      `json:"success"`
	NumTurns                 int       `json:"num_turns"`
	TotalCostUSD             float64   `json:"total_cost_usd"`
	DurationMS               int       `json:"duration_ms"`
	InputTokens              int       `json:"input_tokens"`
	OutputTokens             int       `json:"output_tokens"`
	CacheReadInputTokens     int       `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int       `json:"cache_creation_input_tokens"`
}

// NewRecord builds a Record from a parsed `claude -p` result plus the
// trial's identity (which task/variant it was) and the outcome of running
// the task's check script.
func NewRecord(task, variant string, success bool, cr ClaudeResult) Record {
	return Record{
		Timestamp:                time.Now().UTC(),
		Task:                     task,
		Variant:                  variant,
		SessionID:                cr.SessionID,
		Success:                  success,
		NumTurns:                 cr.NumTurns,
		TotalCostUSD:             cr.TotalCostUSD,
		DurationMS:               cr.DurationMS,
		InputTokens:              cr.Usage.InputTokens,
		OutputTokens:             cr.Usage.OutputTokens,
		CacheReadInputTokens:     cr.Usage.CacheReadInputTokens,
		CacheCreationInputTokens: cr.Usage.CacheCreationInputTokens,
	}
}
