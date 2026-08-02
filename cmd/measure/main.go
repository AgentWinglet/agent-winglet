// Command measure runs one paired-run trial for the harness described in
// agent-winglet-v1-spec.md §5: invoke `claude -p` on a task's prompt inside
// a prepared working directory, score the result against the task's check
// script, and append the outcome to a results log.
//
// measure does not prepare the working directory itself — it doesn't
// install or remove the ledger hook, and it doesn't reset the workdir to
// the task's fixture state. That's harness/setup-workdir.sh's job, so a
// single trial stays a single responsibility: run once, score once, record
// once. See harness/README.md for the full paired-run workflow.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/umitkaanusta/agent-winglet/internal/harness"
)

func main() {
	taskDir := flag.String("task", "", "path to the task directory (must contain prompt.txt and check.sh)")
	workDir := flag.String("workdir", "", "directory to run `claude -p` in; must already be prepared for the chosen variant")
	variant := flag.String("variant", "", "hook or control")
	results := flag.String("results", "harness/results.jsonl", "path to the JSONL results log to append to")
	taskName := flag.String("task-name", "", "label recorded for this trial; defaults to the base name of -task")
	flag.Parse()

	if err := run(*taskDir, *workDir, *variant, *results, *taskName); err != nil {
		fmt.Fprintln(os.Stderr, "measure:", err)
		os.Exit(1)
	}
}

func run(taskDir, workDir, variant, results, taskName string) error {
	if taskDir == "" || workDir == "" || results == "" {
		return fmt.Errorf("-task, -workdir, and -results are required")
	}
	if variant != "hook" && variant != "control" {
		return fmt.Errorf("-variant must be \"hook\" or \"control\", got %q", variant)
	}
	if taskName == "" {
		taskName = filepath.Base(filepath.Clean(taskDir))
	}

	prompt, err := os.ReadFile(filepath.Join(taskDir, "prompt.txt"))
	if err != nil {
		return fmt.Errorf("reading prompt.txt: %w", err)
	}

	claudeOut, claudeErr := runClaude(string(prompt), workDir)
	if claudeErr != nil {
		return fmt.Errorf("running claude: %w", claudeErr)
	}

	cr, err := harness.ParseClaudeResult(claudeOut)
	if err != nil {
		return fmt.Errorf("parsing claude output: %w\noutput was: %s", err, claudeOut)
	}

	// check.sh must be resolved to an absolute path: cmd.Dir below chdirs
	// the child to workDir before exec, so a relative path would be looked
	// up inside workDir (where check.sh doesn't exist) instead of taskDir.
	checkScript, err := filepath.Abs(filepath.Join(taskDir, "check.sh"))
	if err != nil {
		return fmt.Errorf("resolving check.sh path: %w", err)
	}
	success := runCheck(checkScript, workDir)

	rec := harness.NewRecord(taskName, variant, success, cr)
	if err := harness.AppendRecord(results, rec); err != nil {
		return fmt.Errorf("appending result: %w", err)
	}

	fmt.Printf("task=%s variant=%s success=%v turns=%d cost_usd=%.4f session=%s\n",
		rec.Task, rec.Variant, rec.Success, rec.NumTurns, rec.TotalCostUSD, rec.SessionID)
	return nil
}

// runClaude runs `claude -p <prompt> --output-format json` with cwd=workDir
// and returns its stdout. It intentionally does not set --dangerously-skip-
// permissions or any auto-approve flag — a paired trial should exercise the
// same permission prompts (auto-denied when unattended) a real session
// would, not a permission-free fast path that no real usage matches.
func runClaude(prompt, workDir string) ([]byte, error) {
	cmd := exec.Command("claude", "-p", prompt, "--output-format", "json")
	cmd.Dir = workDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w (stderr: %s)", err, stderr.String())
	}
	return stdout.Bytes(), nil
}

// runCheck runs the task's check script with cwd=workDir. Exit code 0 means
// the task was solved; any other exit code (including the script being
// unrunnable) means it wasn't — a broken check script is treated as a
// failed trial, not an error, since a paired run must always produce a
// scored record.
func runCheck(checkScript, workDir string) bool {
	cmd := exec.Command(checkScript)
	cmd.Dir = workDir
	return cmd.Run() == nil
}
