package pricing

import "testing"

func TestLookupReturnsKnownModelRate(t *testing.T) {
	r := Lookup("claude-opus-5")
	if r.Input != 5 || r.Output != 25 {
		t.Fatalf("Lookup(claude-opus-5) = %+v, want Input=5 Output=25", r)
	}
}

func TestLookupFallsBackToSonnetForUnknownModel(t *testing.T) {
	want := table[fallbackModel]
	if got := Lookup("some-model-that-does-not-exist"); got != want {
		t.Fatalf("Lookup(unknown) = %+v, want fallback %+v", got, want)
	}
	if got := Lookup(""); got != want {
		t.Fatalf("Lookup(\"\") = %+v, want fallback %+v", got, want)
	}
}

func TestLookupOpenAIReturnsKnownModelRate(t *testing.T) {
	r := LookupOpenAI("gpt-5-codex")
	if r.Input != 1.25 || r.Output != 10 || r.CacheRead != 0.125 {
		t.Fatalf("LookupOpenAI(gpt-5-codex) = %+v, want Input=1.25 CacheRead=0.125 Output=10", r)
	}
	if r.CacheWrite5m != r.Input || r.CacheWrite1h != r.Input {
		t.Fatalf("OpenAI cache-write rates should match regular input pricing when no separate public write surcharge exists: %+v", r)
	}
}

func TestLookupOpenAINeverFallsBackToClaude(t *testing.T) {
	got := LookupOpenAI("codex-auto-review")
	want := openAITable[fallbackOpenAIModel]
	if got != want {
		t.Fatalf("LookupOpenAI(unknown) = %+v, want OpenAI fallback %+v", got, want)
	}
	if got == table[fallbackClaudeModel] {
		t.Fatalf("LookupOpenAI(unknown) fell through to Claude fallback: %+v", got)
	}
}

func TestLookupOpenAINormalizesSnapshotSuffix(t *testing.T) {
	got := LookupOpenAI("gpt-5.5-2026-04-23")
	want := openAITable["gpt-5.5"]
	if got != want {
		t.Fatalf("LookupOpenAI(snapshot) = %+v, want %+v", got, want)
	}
}

func TestEveryRateHasNonZeroFields(t *testing.T) {
	for model, r := range table {
		if r.Input <= 0 || r.Output <= 0 || r.CacheRead <= 0 || r.CacheWrite5m <= 0 || r.CacheWrite1h <= 0 {
			t.Fatalf("model %q has a zero/negative rate field: %+v", model, r)
		}
	}
	for model, r := range openAITable {
		if r.Input <= 0 || r.Output <= 0 || r.CacheRead <= 0 || r.CacheWrite5m <= 0 || r.CacheWrite1h <= 0 {
			t.Fatalf("OpenAI model %q has a zero/negative rate field: %+v", model, r)
		}
	}
}
