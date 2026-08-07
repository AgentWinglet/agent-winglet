// Package codexrollout reads Codex rollout JSONL files and summarizes them
// into the same usage shape used by Claude transcript parsing.
//
// Codex's rollout format is explicitly not a stable hook interface, so this
// parser is deliberately narrow, fixture-backed, and fail-soft: unreadable,
// malformed, or partially-written files should not make a hook fail.
package codexrollout

import (
	"bufio"
	"encoding/json"
	"io"
	"os"

	"github.com/umitkaanusta/agent-winglet/internal/pricing"
	"github.com/umitkaanusta/agent-winglet/internal/transcript"
)

// ReadSessionUsage streams path and returns its full cumulative usage. A
// missing, unreadable, or malformed file returns zero usage and a nil error.
func ReadSessionUsage(path string) (transcript.SessionUsage, error) {
	u, _, err := ReadSessionUsageWithOffset(path)
	return u, err
}

// ReadSessionUsageWithOffset is the full-file reconciliation read. It counts
// a final non-newline-terminated line because there is no later incremental
// read that can safely pick it up.
func ReadSessionUsageWithOffset(path string) (transcript.SessionUsage, int64, error) {
	if path == "" {
		return transcript.SessionUsage{}, 0, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return transcript.SessionUsage{}, 0, nil
	}
	defer f.Close()

	var acc accumulator
	reader := bufio.NewReaderSize(f, 64*1024)
	var pos int64
	for {
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			accumulateLine(line, &acc)
			pos += int64(len(line))
		}
		if err != nil {
			break
		}
	}

	return acc.usage(), pos, nil
}

// ReadSessionUsageFrom reads complete JSONL lines written since offset and
// returns the usage delta relative to previous. Codex token_count events are
// cumulative, unlike Claude assistant usage rows, so previous is required to
// avoid double-counting when callers add the returned delta onto session
// stats. A trailing line without a newline is left unconsumed for a later
// call.
func ReadSessionUsageFrom(path string, offset int64, previous transcript.SessionUsage) (transcript.SessionUsage, int64, error) {
	if path == "" {
		return transcript.SessionUsage{}, offset, nil
	}
	if offset < 0 {
		offset = 0
	}

	f, err := os.Open(path)
	if err != nil {
		return transcript.SessionUsage{}, offset, nil
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return transcript.SessionUsage{}, offset, nil
		}
	}

	var acc accumulator
	reader := bufio.NewReaderSize(f, 64*1024)
	pos := offset
	for {
		line, err := reader.ReadBytes('\n')
		if err == nil {
			pos += int64(len(line))
			accumulateLine(line, &acc)
			continue
		}
		if err == io.EOF {
			break
		}
		break
	}

	delta := transcript.SessionUsage{ContentBytes: acc.contentBytes}
	if acc.hasTokenUsage {
		latest := acc.tokenUsage
		delta.Tokens = nonNegative(latest.Tokens - previous.Tokens)
		delta.CostUSD = nonNegativeFloat(latest.CostUSD - previous.CostUSD)
	}
	return delta, pos, nil
}

type accumulator struct {
	model         string
	contentBytes  int64
	hasTokenUsage bool
	tokenUsage    transcript.SessionUsage
}

func (a *accumulator) usage() transcript.SessionUsage {
	u := transcript.SessionUsage{ContentBytes: a.contentBytes}
	if a.hasTokenUsage {
		u.Tokens = a.tokenUsage.Tokens
		u.CostUSD = a.tokenUsage.CostUSD
	}
	return u
}

type rolloutLine struct {
	Type    string  `json:"type"`
	Payload payload `json:"payload"`
}

type payload struct {
	Type    string          `json:"type"`
	Role    string          `json:"role"`
	Model   string          `json:"model"`
	Content json.RawMessage `json:"content"`
	Output  string          `json:"output"`
	Info    struct {
		TotalTokenUsage codexTokenUsage `json:"total_token_usage"`
	} `json:"info"`
}

type codexTokenUsage struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
	TotalTokens           int64 `json:"total_tokens"`
}

type contentPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func accumulateLine(line []byte, acc *accumulator) {
	var rl rolloutLine
	if err := json.Unmarshal(line, &rl); err != nil {
		return
	}
	if rl.Payload.Model != "" {
		acc.model = rl.Payload.Model
	}

	switch {
	case rl.Type == "event_msg" && rl.Payload.Type == "token_count":
		acc.hasTokenUsage = true
		acc.tokenUsage = priceTokenUsage(rl.Payload.Info.TotalTokenUsage, acc.model)
	case rl.Type == "response_item" && rl.Payload.Type == "message" && rl.Payload.Role == "user":
		acc.contentBytes += contentTextBytes(rl.Payload.Content)
	case rl.Type == "response_item" && rl.Payload.Type == "function_call_output":
		acc.contentBytes += int64(len([]byte(rl.Payload.Output)))
	}
}

func priceTokenUsage(u codexTokenUsage, model string) transcript.SessionUsage {
	rate := pricing.LookupOpenAI(model)
	included := u.InputTokens + u.CacheWriteInputTokens
	cost := float64(u.InputTokens)*rate.Input/1e6 +
		float64(u.CacheWriteInputTokens)*rate.CacheWrite5m/1e6
	return transcript.SessionUsage{Tokens: included, CostUSD: cost}
}

func contentTextBytes(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}

	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return int64(len([]byte(s)))
	}

	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return 0
	}
	var n int64
	for _, part := range parts {
		n += int64(len([]byte(part.Text)))
	}
	return n
}

func nonNegative(n int64) int64 {
	if n < 0 {
		return 0
	}
	return n
}

func nonNegativeFloat(f float64) float64 {
	if f < 0 {
		return 0
	}
	return f
}
