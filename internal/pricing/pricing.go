// Package pricing is an embedded, no-network-fetch table of USD-per-million-
// token rates for the models agent-winglet is likely to see in a
// transcript's message.model field. It exists to price already-known
// suppressed-byte counts at real rates (see internal/transcript and
// spec.md's §2 framing of "$X of tool output never sent," not "saved you
// $X") — not to re-measure total session cost, which the deleted
// internal/harness paired-run gate already tried and found inconclusive.
package pricing

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

// fallbackModel is used whenever a transcript reports a model string this
// table doesn't recognize — same fail-soft convention internal/stats already
// uses for a corrupt stats file: degrade to a reasonable default rather than
// erroring the whole read.
const fallbackModel = "claude-sonnet-5"

// table is USD per million tokens. Sonnet 5's $2/$10 input/output rate is
// promo pricing confirmed through 2026-08-31; this table isn't wired to any
// auto-expiring switch (out of scope — revisit if pricing drifts noticeably
// before then, per spec.md §3).
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

// Lookup returns model's rate, falling back to fallbackModel's rate when
// model is empty or unrecognized (a transcript line missing message.model,
// or a model string this table predates).
func Lookup(model string) Rate {
	if r, ok := table[model]; ok {
		return r
	}
	return table[fallbackModel]
}
