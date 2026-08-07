// Package pricing is an embedded, no-network-fetch table of USD-per-million-
// token rates for the models agent-winglet is likely to see in transcript
// files. It exists to price already-known suppressed-byte counts at real
// rates (see internal/transcript and internal/codexrollout) — framed as
// "$X of tool output never sent," not "saved you $X": a paired-run test of
// total session cost came back inconclusive, so no such claim is made here.
package pricing

import "strings"

// Rate is one model's per-million-token USD pricing, split by token type.
// CacheRead5m/CacheWrite1h name the two cache-token rates a transcript's
// usage block can report (cache_read_input_tokens, cache_creation_input_
// tokens' 5m/1h split) — CacheWrite5m is derived (1.25x input) rather than
// independently tabulated, since every model in this table prices 5m writes
// as a fixed multiple of input, not an independent figure.
type Rate struct {
	Input        float64 // USD per million input tokens
	Output       float64 // USD per million output tokens
	CacheRead    float64 // USD per million cache-read tokens (10% of Input)
	CacheWrite5m float64 // USD per million 5-minute cache-write tokens (1.25x Input)
	CacheWrite1h float64 // USD per million 1-hour cache-write tokens (2x Input)
}

const (
	// fallbackModel is kept for compatibility with tests and callers that use
	// Lookup's Claude-specific behavior.
	fallbackModel       = fallbackClaudeModel
	fallbackClaudeModel = "claude-sonnet-5"
	fallbackOpenAIModel = "gpt-5-codex"
)

// table is USD per million tokens. Sonnet 5's $2/$10 input/output rate is
// promo pricing confirmed through 2026-08-31; this table isn't wired to any
// auto-expiring switch (out of scope — revisit if pricing drifts noticeably
// before then).
var table = map[string]Rate{
	"claude-opus-5": {
		Input: 5, Output: 25,
		CacheRead: 0.5, CacheWrite5m: 6.25, CacheWrite1h: 10,
	},
	"claude-sonnet-5": {
		Input: 2, Output: 10,
		CacheRead: 0.2, CacheWrite5m: 2.5, CacheWrite1h: 4,
	},
	"claude-haiku-4-5": {
		Input: 1, Output: 5,
		CacheRead: 0.1, CacheWrite5m: 1.25, CacheWrite1h: 2,
	},
	"claude-fable-5": {
		Input: 10, Output: 50,
		CacheRead: 1.0, CacheWrite5m: 12.5, CacheWrite1h: 20,
	},
}

// openAITable is USD per million tokens, sourced from official OpenAI model
// pricing pages on 2026-08-07. OpenAI's public pricing exposes regular input,
// cached input, and output rates, but not a separate cache-write surcharge for
// these models; cache-write tokens are therefore priced at the regular input
// rate. That keeps cache_write_input_tokens in the same "new content" bucket
// as uncached input tokens without inventing an unpublished write premium.
var openAITable = map[string]Rate{
	"codex-mini-latest":   openAIRate(1.50, 6.00, 0.375),
	"gpt-5-codex":         openAIRate(1.25, 10.00, 0.125),
	"gpt-5":               openAIRate(1.25, 10.00, 0.125),
	"gpt-5-chat-latest":   openAIRate(1.25, 10.00, 0.125),
	"gpt-5.1":             openAIRate(1.25, 10.00, 0.125),
	"gpt-5.1-chat-latest": openAIRate(1.25, 10.00, 0.125),
	"gpt-5.2":             openAIRate(1.75, 14.00, 0.175),
	"gpt-5.2-chat-latest": openAIRate(1.75, 14.00, 0.175),
	"gpt-5.4":             openAIRate(2.50, 15.00, 0.25),
	"gpt-5.4-mini":        openAIRate(0.75, 4.50, 0.075),
	"gpt-5.5":             openAIRate(5.00, 30.00, 0.50),
	"gpt-5.6":             openAIRate(5.00, 30.00, 0.50),
	"gpt-5.6-sol":         openAIRate(5.00, 30.00, 0.50),
	"gpt-5.6-terra":       openAIRate(2.50, 15.00, 0.25),
	"gpt-5.6-luna":        openAIRate(1.00, 6.00, 0.10),
	"chat-latest":         openAIRate(5.00, 30.00, 0.50),
}

func openAIRate(input, output, cachedInput float64) Rate {
	return Rate{
		Input:        input,
		Output:       output,
		CacheRead:    cachedInput,
		CacheWrite5m: input,
		CacheWrite1h: input,
	}
}

// Lookup returns model's Claude rate, falling back to fallbackModel's rate
// when model is empty or unrecognized (a transcript line missing
// message.model, or a model string this table predates).
func Lookup(model string) Rate {
	return LookupClaude(model)
}

// LookupClaude returns model's Claude rate and never consults OpenAI rates.
func LookupClaude(model string) Rate {
	if r, ok := table[model]; ok {
		return r
	}
	return table[fallbackClaudeModel]
}

// LookupOpenAI returns model's OpenAI rate and never consults Claude rates.
// Snapshot suffixes like gpt-5.5-2026-04-23 are normalized to their base
// model when that base model is known.
func LookupOpenAI(model string) Rate {
	if r, ok := openAITable[model]; ok {
		return r
	}
	if base := openAIBaseModel(model); base != model {
		if r, ok := openAITable[base]; ok {
			return r
		}
	}
	return openAITable[fallbackOpenAIModel]
}

func openAIBaseModel(model string) string {
	if model == "" {
		return model
	}
	for known := range openAITable {
		if strings.HasPrefix(model, known+"-20") {
			return known
		}
	}
	return model
}
