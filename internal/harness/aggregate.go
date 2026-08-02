package harness

import "fmt"

// Group is the usage_per_solve computation for one (task, variant) pair
// across every recorded trial.
type Group struct {
	Task         string
	Variant      string
	Runs         int
	Successes    int
	TotalCostUSD float64
	TotalTurns   int
}

// UsagePerSolve is (usage consumed) / (tasks completed successfully), the
// metric agent-winglet-v1-spec.md §5 requires before any lever counts as
// validated. ok is false when there are zero successful runs — dividing by
// a zero denominator would silently hide a variant that never solved the
// task at all, which is a worse outcome than any usage number could offset.
func (g Group) UsagePerSolve() (cost float64, ok bool) {
	if g.Successes == 0 {
		return 0, false
	}
	return g.TotalCostUSD / float64(g.Successes), true
}

func (g Group) String() string {
	cost, ok := g.UsagePerSolve()
	if !ok {
		return fmt.Sprintf("%s/%s: %d runs, 0 successes — usage_per_solve undefined", g.Task, g.Variant, g.Runs)
	}
	avgTurns := float64(g.TotalTurns) / float64(g.Runs)
	return fmt.Sprintf("%s/%s: %d/%d succeeded, usage_per_solve=$%.4f, avg turns=%.1f",
		g.Task, g.Variant, g.Successes, g.Runs, cost, avgTurns)
}

// Aggregate groups records by (task, variant) and sums each group's totals.
// Group order follows first appearance in records.
func Aggregate(records []Record) []Group {
	type key struct{ task, variant string }
	index := map[key]*Group{}
	var order []key
	for _, r := range records {
		k := key{r.Task, r.Variant}
		g, exists := index[k]
		if !exists {
			g = &Group{Task: r.Task, Variant: r.Variant}
			index[k] = g
			order = append(order, k)
		}
		g.Runs++
		if r.Success {
			g.Successes++
		}
		g.TotalCostUSD += r.TotalCostUSD
		g.TotalTurns += r.NumTurns
	}
	groups := make([]Group, 0, len(order))
	for _, k := range order {
		groups = append(groups, *index[k])
	}
	return groups
}
