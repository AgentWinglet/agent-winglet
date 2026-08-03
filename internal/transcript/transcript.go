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
	// Tokens is the input-side token total across every assistant line, but
	// only the tokens that represent content newly fed to the model on that
	// line: input_tokens + cache_creation (5m+1h) tokens. This is the same
	// bucket suppressed tool-output content would land in if it hadn't been
	// suppressed.
	//
	// cache_read_input_tokens is deliberately excluded, and that exclusion
	// is load-bearing, not incidental: a long session re-reads its entire
	// growing context from cache on every later turn, so summing cache-read
	// tokens across all turns counts the same underlying content once for
	// every turn that came after it. ContentBytes (below), by contrast,
	// counts each user-role line's content exactly once, whenever it first
	// appears. Mixing a quantity that scales with turn count into a ratio
	// against a quantity that doesn't produces a bytes-to-tokens (and later
	// cost-to-bytes) ratio that inflates with session length rather than
	// tracking actual content size — this is what turned 3.5 MiB of
	// suppressed content into a $18+ estimate for a long, many-turn
	// session. output_tokens (the model's own generated text) is excluded
	// for the same "not this content" reason.
	Tokens int64
	// CostUSD is the priced cost of Tokens only — input_tokens plus
	// cache_creation (5m+1h) tokens, each at internal/pricing's embedded
	// rate for the line's own model. cache_read_input_tokens' cost and
	// output_tokens' cost are excluded for the same reason Tokens excludes
	// them: both would price something other than the content ContentBytes
	// counts, and cache-read cost in particular would scale with how many
	// later turns replayed this content from cache, not with the content
	// itself.
	CostUSD float64
	// ContentBytes is the raw JSON-encoded byte length of message.content
	// across every user-role line (covers tool_result content Claude Code
	// echoes back into the transcript) — the real, this-session bytes-to-
	// tokens ratio, not a generic chars/4 estimate. Each line is counted
	// once, the same "counted once" scale as Tokens and CostUSD above —
	// that shared scale is what makes CostUSD/ContentBytes (or the
	// equivalent Tokens/ContentBytes then CostUSD/Tokens path) a stable
	// per-byte price instead of one that drifts with session length.
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

			tokens := us.InputTokens + cacheCreation1h + cacheCreation5m
			u.Tokens += tokens

			cost := float64(us.InputTokens)*rate.Input/1e6 +
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
