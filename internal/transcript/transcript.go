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
	"io"
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
		accumulateLine(scanner.Bytes(), &u)
	}
	if err := scanner.Err(); err != nil {
		return SessionUsage{}, nil
	}

	return u, nil
}

// accumulateLine parses one JSONL transcript line and folds it into u, the
// same per-line logic ReadSessionUsage and ReadSessionUsageFrom both need. A
// malformed line is skipped, not an error — matches ReadSessionUsage's
// existing fail-soft convention.
func accumulateLine(line []byte, u *SessionUsage) {
	var tl transcriptLine
	if err := json.Unmarshal(line, &tl); err != nil {
		return
	}

	switch {
	case tl.Type == "assistant" && tl.Message.Usage != nil:
		rate := pricing.Lookup(tl.Message.Model)
		us := tl.Message.Usage
		cacheCreation1h := us.CacheCreation.Ephemeral1hInputTokens
		cacheCreation5m := us.CacheCreation.Ephemeral5mInputTokens

		tokens := us.InputTokens + cacheCreation1h + cacheCreation5m
		u.Tokens += tokens

		cost := float64(us.InputTokens)*rate.Input/1e6 +
			float64(cacheCreation5m)*rate.CacheWrite5m/1e6 +
			float64(cacheCreation1h)*rate.CacheWrite1h/1e6
		u.CostUSD += cost

	case tl.Type == "user":
		u.ContentBytes += int64(len(tl.Message.Content))
	}
}

// ReadSessionUsageFrom reads only the transcript content written since
// offset — the incremental counterpart to ReadSessionUsage, built so a hook
// that fires on every tool call (PostToolUse) can keep a session's usage
// tally current without re-parsing the whole, ever-growing transcript file
// from scratch on every single call. Returns the delta usage for just the
// newly-read content and the byte offset to pass back in on the next call;
// the caller is expected to accumulate the delta onto a running total (see
// stats.Session.AddTranscriptUsage), not treat it as the session's full
// usage.
//
// Unlike ReadSessionUsage's bufio.Scanner (which returns a final
// non-newline-terminated token at EOF as-is), this uses bufio.Reader.
// ReadBytes so a line still mid-write when this runs — real, since Claude
// Code appends to the transcript live while a hook may run concurrently —
// is detected and left unconsumed: the returned offset never advances past
// an incomplete final line, so that line's content is read whole on a later
// call instead of being silently split or lost. A one-shot read (like
// ReadSessionUsage's, called once at SessionEnd) doesn't need this care,
// since there's no "later call" to hand an unfinished line to.
func ReadSessionUsageFrom(path string, offset int64) (SessionUsage, int64, error) {
	var u SessionUsage
	if path == "" {
		return u, offset, nil
	}

	f, err := os.Open(path)
	if err != nil {
		return SessionUsage{}, offset, nil
	}
	defer f.Close()

	if offset > 0 {
		if _, err := f.Seek(offset, io.SeekStart); err != nil {
			return SessionUsage{}, offset, nil
		}
	}

	reader := bufio.NewReaderSize(f, 64*1024)
	pos := offset
	for {
		line, err := reader.ReadBytes('\n')
		if err == nil {
			// Complete line, newline included — safe to consume and count
			// toward the offset.
			pos += int64(len(line))
			accumulateLine(line, &u)
			continue
		}
		if err == io.EOF {
			// A trailing chunk with no newline is either the true end of a
			// complete write (nothing more coming, but we can't tell that
			// from here) or a write still in progress. Either way, don't
			// advance pos past it and don't count it now — a future call
			// picks it up whole once the newline lands. len(line) == 0 at
			// true EOF just means there was nothing new to read.
			break
		}
		// Unexpected read error: stop here, keep whatever was accumulated
		// and the offset up to the last fully-consumed line, matching this
		// package's fail-soft convention elsewhere.
		break
	}

	return u, pos, nil
}
