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

func TestEveryRateHasNonZeroFields(t *testing.T) {
	for model, r := range table {
		if r.Input <= 0 || r.Output <= 0 || r.CacheRead <= 0 || r.CacheWrite5m <= 0 || r.CacheWrite1h <= 0 {
			t.Fatalf("model %q has a zero/negative rate field: %+v", model, r)
		}
	}
}
