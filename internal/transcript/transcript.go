// Package transcript reads a Claude Code session's transcript JSONL file
// (the path Claude Code sends as transcript_path on every hook event) and
// summarizes it into the real input-side token count, its cost at real
// per-token rates, and the raw content-byte size those tokens represent —
// the same data ccusage reads, used here to price already-known suppressed-
// byte counts (see spec.md §3), not to re-measure total session cost.
package transcript

import (
	"bufio"
	"encoding/json"
	"os"

	"github.com/umitkaanusta/agent-winglet/internal/pricing"
)

// SessionUsage is one transcript file's summary.
type SessionUsage struct {
	// Tokens is the input-side token total across every assistant line:
	// input + cache_read + cache_creation (5m+1h) tokens, summed per line
	// using that line's own model (sessions can mix models). This is the
	// same bucket suppressed tool-output content would land in if it hadn't
	// been suppressed — output_tokens (the model's own generated text) is
	// deliberately excluded.
	Tokens int64
	// CostUSD is the priced cost of Tokens (plus each line's own output-
	// token cost), summed the same per-line way, at internal/pricing's
	// embedded rates for the model actually used.
	CostUSD float64
	// ContentBytes is the raw JSON-encoded byte length of message.content
	// across every user-role line (covers tool_result content Claude Code
	// echoes back into the transcript) — the real, this-session bytes-to-
	// tokens ratio, not a generic chars/4 estimate.
	ContentBytes int64
}

// transcriptLine is the subset of one JSONL line this package reads. Only
// the fields needed to compute SessionUsage are declared; everything else
// in a real transcript line is ignored.
type transcriptLine struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Model   string          `json:"model"`
		Content json.RawMessage `json:"content"`
		Usage   *usageBlock     `json:"usage"`
	} `json:"message"`
}

type usageBlock struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheCreation            struct {
		Ephemeral5mInputTokens int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1hInputTokens int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

// ReadSessionUsage streams path line by line (transcript files run 400KB+,
// so the whole file is never held in memory at once) and summarizes it. A
// missing, unreadable, or malformed file returns an all-zero SessionUsage
// and a nil error — same fail-soft convention internal/stats already uses
// for a corrupt state file: a session-end hook shouldn't fail just because
// it can't price this session's savings.
func ReadSessionUsage(path string) (SessionUsage, error) {
	var u SessionUsage
	if path == "" {
		return u, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return SessionUsage{}, nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		var line transcriptLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}

		switch {
		case line.Type == "assistant" && line.Message.Usage != nil:
			rate := pricing.Lookup(line.Message.Model)
			us := line.Message.Usage
			cacheCreation1h := us.CacheCreation.Ephemeral1hInputTokens
			cacheCreation5m := us.CacheCreation.Ephemeral5mInputTokens

			tokens := us.InputTokens + us.CacheReadInputTokens + cacheCreation1h + cacheCreation5m
			u.Tokens += tokens

			cost := float64(us.InputTokens)*rate.Input/1e6 +
				float64(us.OutputTokens)*rate.Output/1e6 +
				float64(us.CacheReadInputTokens)*rate.CacheRead/1e6 +
				float64(cacheCreation5m)*rate.CacheWrite5m/1e6 +
				float64(cacheCreation1h)*rate.CacheWrite1h/1e6
			u.CostUSD += cost

		case line.Type == "user":
			u.ContentBytes += int64(len(line.Message.Content))
		}
	}
	if err := scanner.Err(); err != nil {
		return SessionUsage{}, nil
	}

	return u, nil
}
