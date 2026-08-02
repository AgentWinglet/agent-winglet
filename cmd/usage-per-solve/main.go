// Command usage-per-solve reads a harness results log and reports
// usage_per_solve — (usage consumed) / (tasks completed successfully) —
// per (task, variant), the metric agent-winglet-v1-spec.md §5 requires
// before any context-saving lever counts as validated.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/umitkaanusta/agent-winglet/internal/harness"
)

func main() {
	results := flag.String("results", "harness/results.jsonl", "path to the JSONL results log")
	flag.Parse()

	records, err := harness.ReadRecords(*results)
	if err != nil {
		fmt.Fprintln(os.Stderr, "usage-per-solve:", err)
		os.Exit(1)
	}
	if len(records) == 0 {
		fmt.Println("no records in", *results)
		return
	}

	for _, g := range harness.Aggregate(records) {
		fmt.Println(g.String())
	}
}
