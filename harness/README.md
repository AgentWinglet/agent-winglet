# Measurement harness

Implements the paired-run gate from `~/agent-winglet-v1-spec.md` §5: before
any lever in this repo counts as validated, it must be measured, not just
observed to "mechanically work as designed." See
`agent-winglet-v1-remaining.md` §2.2 for what's built vs. still open.

```
usage_per_solve = (usage consumed) / (tasks completed successfully)
```

## Layout

- `tasks/<name>/prompt.txt` — the prompt fed to `claude -p`.
- `tasks/<name>/fixture/` — files copied into a fresh working directory
  before each trial, so every trial (both variants, every repeat) starts
  from identical state.
- `tasks/<name>/check.sh` — run against the working directory after the
  trial; exit 0 means the task was solved.
- `setup-workdir.sh <task-dir> <workdir> <hook|control>` — resets a working
  directory to fixture state and configures `.claude/settings.json` for the
  requested variant (hook installed, or not).
- `run-paired.sh <task-name> [results-path]` — runs one control trial and
  one hook trial for a task, back to back, appending both to the results
  log.
- `results.jsonl` — accumulated trial records (gitignored; this is local
  measurement data, not a repo artifact).

## Running a trial

```
make build                       # builds bin/ledger-hook and bin/measure
./harness/run-paired.sh fix-typo
bin/usage-per-solve
```

`run-paired.sh` calls `claude -p` for real — it costs real usage and takes
as long as the task takes. It does not pass any auto-approve flag, so a
task whose prompt requires a tool Claude Code would normally prompt for
will stall or fail in a non-interactive trial; keep task prompts scoped to
what an unattended `-p` run can actually complete.

Run each task's pair many times, not once, before drawing any conclusion —
a single pair is one data point, not a measurement.

## Reading the numbers

`bin/usage-per-solve` prints, per (task, variant): success rate, average
turns, and `usage_per_solve` (total `total_cost_usd` across successful and
failed runs alike, divided by the successful-run count). A variant with
fewer successes needs a *lower* cost to justify itself, not just a lower
cost per attempt — that's why the denominator is successes, not runs.

## Known limitation — read before treating this as gate-closing

`total_cost_usd` (from `claude -p --output-format json`) is Anthropic's own
computed cost from real token usage and model pricing. It is the closest
proxy to real usage available to a script, but the spec explicitly asks for
measurement "against actual weekly-cap consumption (not estimated
tokens)" — and weekly-cap percentage is an account-level number, not
something `claude -p` exposes per invocation. Treat `usage_per_solve` from
this harness as a strong leading indicator, not the final word: before
declaring the §5 gate passed, cross-check a batch of trials against the
actual weekly-cap delta shown in the Claude Code UI (`/status` or the usage
page) for the account running them.
