package harness

import "testing"

func TestAggregate_GroupsByTaskAndVariant(t *testing.T) {
	records := []Record{
		{Task: "fix-typo", Variant: "hook", Success: true, TotalCostUSD: 0.10, NumTurns: 4},
		{Task: "fix-typo", Variant: "hook", Success: true, TotalCostUSD: 0.08, NumTurns: 3},
		{Task: "fix-typo", Variant: "control", Success: true, TotalCostUSD: 0.15, NumTurns: 5},
		{Task: "other-task", Variant: "hook", Success: false, TotalCostUSD: 0.20, NumTurns: 6},
	}

	groups := Aggregate(records)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}

	byKey := map[string]Group{}
	for _, g := range groups {
		byKey[g.Task+"/"+g.Variant] = g
	}

	hook := byKey["fix-typo/hook"]
	if hook.Runs != 2 || hook.Successes != 2 {
		t.Fatalf("fix-typo/hook = %+v, want Runs=2 Successes=2", hook)
	}
	cost, ok := hook.UsagePerSolve()
	if !ok {
		t.Fatalf("fix-typo/hook UsagePerSolve() ok=false, want true")
	}
	if want := 0.18 / 2; cost < want-1e-9 || cost > want+1e-9 {
		t.Errorf("fix-typo/hook UsagePerSolve() = %v, want %v", cost, want)
	}

	other := byKey["other-task/hook"]
	if _, ok := other.UsagePerSolve(); ok {
		t.Errorf("other-task/hook UsagePerSolve() ok=true with zero successes, want false")
	}
}

func TestAggregate_Empty(t *testing.T) {
	if got := Aggregate(nil); len(got) != 0 {
		t.Errorf("Aggregate(nil) = %v, want empty", got)
	}
}

func TestGroup_String_ZeroSuccesses(t *testing.T) {
	g := Group{Task: "t", Variant: "control", Runs: 2, Successes: 0}
	got := g.String()
	want := "t/control: 2 runs, 0 successes — usage_per_solve undefined"
	if got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}
