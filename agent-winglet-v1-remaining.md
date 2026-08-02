# SPEC: Agent Winglet v1 — what's left

Companion to `~/agent-winglet-v1-spec.md`. That spec defines all of v1; this
file tracks what's built vs. outstanding, so it doesn't need re-deriving
from git history each session.

## 1. Done

**Session Ledger, Bash-only** (`agent-winglet-v1-spec.md` §4.1):

- `cmd/ledger-hook` — Go binary registered as a `PostToolUse` (matcher:
  `Bash`), `SessionStart`, and `PostCompact` hook.
- `internal/ledger` — per-session state file at
  `.claude/agent-winglet/<session_id>.json`, holding a hash + turn number
  per distinct Bash command seen this session.
- On an exact-repeat Bash command (same command, same stdout, no stderr,
  not interrupted, not an image), the hook emits `updatedToolOutput` to
  replace stdout with `[agent-winglet] unchanged since turn N (...)`.
- `SessionStart`/`PostCompact` delete the ledger file outright — satisfies
  the spec's hard constraint that a substitution must never survive a
  session restart or compaction.
- Verified live against real `claude -p` sessions: substitution fires
  correctly on repeat, and a new session (different `session_id`) never
  sees a prior session's ledger.
- `go test ./...` covers the ledger logic and hook decision logic
  (`internal/ledger/ledger_test.go`, `cmd/ledger-hook/main_test.go`):
  repeat detection, turn counting, disk round-trip, SessionStart/PostCompact
  invalidation, session isolation, and the interrupted/isImage/stderr
  skip-conditions. All run against `t.TempDir()` — no dependency on the
  gitignored `bin/` binary. Does NOT cover real Claude Code integration
  (payload shape drift, whether hooks actually fire as configured) — that
  still requires the manual `claude -p` smoke test described above.

**Scope correction made during build**: Read was dropped from the ledger.
Claude Code already returns `tool_response.type == "file_unchanged"`
natively on a repeat identical Read, confirmed by inspecting real
`PostToolUse` payloads. Building ledger logic for Read would have
duplicated an existing harness capability. Only Bash lacks this, so only
Bash is handled.

## 2. Not started

### 2.1 Context lifecycle hooks (spec §4.2)

None of the four levers are built yet:

- **Retire used-up context at phase boundaries.** No mechanism yet to
  detect a phase boundary (e.g. "investigation done, implementation
  starting") or to replace a stale file-read/test-log with a compact
  receipt once it's served its purpose. Distinct from the Ledger's
  exact-repeat case — this is for content read once, used, and now stale.
- **Trim tool/schema definitions to task phase.** No mechanism to defer
  tool schemas the current phase won't call.
- **Budget output by outcome.** No logic to shrink a passing command's
  output vs. keeping a failing command's full trace. Current Bash handling
  in the Ledger already refuses to touch stderr/interrupted/image cases,
  but doesn't yet do anything active for the success case beyond the
  exact-repeat check.
- **Compact at investigate→implement boundary.** No hook exists yet that
  triggers or suggests compaction at a natural phase transition rather
  than at the context-window cliff.

Each of these needs its own design pass — they weren't scoped or
prototyped alongside the Ledger.

### 2.2 Measurement gate (spec §5) — do not skip

This is the actual gate before the tool is anything more than a personal
toy, per the full spec's Phase 2 rule.

**Built:**

- `internal/harness` — `Record`/`ClaudeResult` types, `Aggregate` (groups
  trials by task+variant and computes `usage_per_solve = total_cost_usd /
  successes`, `ok=false` when a variant has zero successes rather than
  hiding a total failure behind a divide), and a JSONL results log
  (`AppendRecord`/`ReadRecords`). Unit tested, no dependency on a real
  `claude` binary.
- `cmd/measure` — runs one paired-run trial: `claude -p <prompt>
  --output-format json` in a prepared working directory, scores it against
  the task's `check.sh`, appends a `Record`. Smoke-tested end-to-end with a
  stubbed `claude` binary (both the success and failure scoring paths) —
  caught and fixed a real bug where `check.sh`'s path was resolved
  relative to the *parent's* cwd but executed with `cmd.Dir` pointed at the
  workdir, so the script was never found and every trial silently scored
  as a failure.
- `cmd/usage-per-solve` — reads the results log, prints per-(task,variant)
  success rate, avg turns, and `usage_per_solve`.
- `harness/setup-workdir.sh` + `harness/run-paired.sh` — reset a working
  directory to task fixture state and configure (or omit) the ledger hook
  per variant; run one control + one hook trial back to back.
- `harness/tasks/fix-typo/` — one example task (fixture Go package with a
  bug, a prompt that nudges a repeated identical `go build ./...` call so
  the ledger hook has something to act on, and a `check.sh`) proving the
  harness runs end-to-end.
- `harness/README.md` documents the workflow and flags the one real
  limitation: `total_cost_usd` from `claude -p` is Anthropic's computed
  cost, the closest scriptable proxy to usage — but the spec asks for
  measurement against actual weekly-cap consumption, which isn't
  exposable per-invocation. Treat `usage_per_solve` from this harness as a
  strong leading indicator, not gate-closing on its own.

**Still not started:**

- No real trial data. Every piece above has been exercised with a stubbed
  `claude` binary, never a live `claude -p` run — running `fix-typo` for
  real against both variants, repeated enough times to mean something, is
  the next step.
- No cross-check of a batch of real trials against actual weekly-cap
  percentage in the Claude Code UI, which `harness/README.md` calls out as
  required before the gate counts as closed.
- Only one task exists (`fix-typo`). It exercises exactly one Ledger
  scenario (an identical repeated Bash command). More tasks are needed to
  cover other waste-taxonomy cases before the gate says anything general.

**Until real trial data exists, nothing in §1 should be treated as
validated —** only as "mechanically works as designed," now with a harness
ready to generate the data that would actually validate it.

### 2.3 Housekeeping — done

- `README.md` documents both install (end users, via `install.sh`) and dev
  setup (`make build`/`make test`) for this repo.
- `Makefile` added (`make build`, `make test`).
- `install.sh` added: runs `go install .../cmd/ledger-hook@latest`, then
  merges (not overwrites) hook entries into the target project's
  `.claude/settings.json` via `jq`. Verified the merge logic preserves an
  existing `permissions.allow` block untouched. The `go install` half is
  NOT yet verified live — `go install .../cmd/ledger-hook@latest` resolves
  against the repo's default branch, and these files are currently only on
  `add-v1-smth` (pushed to `origin`, not yet merged to `main`).
- Clarified the split: this repo's own `.claude/settings.json` is a
  dev/test fixture (used to exercise the hook against this repo itself,
  same pattern as e.g. `headroom-desktop`'s `.claude/settings.json` for
  permission allowlisting) — it is NOT the end-user install mechanism.
  `install.sh` is.

### 2.3.1 Still open

- `.github/workflows/ci.yml` added: runs `go build ./...`, `go vet ./...`,
  a `gofmt -l` check, and `go test ./...` on push to `main` and on PRs.
  Not yet verified against a real GitHub Actions run — that only happens
  once `add-v1-smth` is pushed as a PR or merged to `main`.
- `install.sh`'s `go install` path is still unverified against a real
  `go install @latest` — needs `add-v1-smth` (or its contents) to land on
  `main` first, since `@latest` resolves against the default branch.
- Current work is on branch `add-v1-smth`, pushed to `origin` but not yet
  merged to `main`.
- Fixed: `install.sh`'s jq merge now checks whether an entry referencing
  the same hook command already exists in `PostToolUse`/`SessionStart`/
  `PostCompact` before appending, so running the script twice in the same
  project is a no-op the second time instead of duplicating entries.
  Verified locally by running the merge twice against a fixture
  `settings.json` and diffing the output.

## 3. Suggested order

1. Housekeeping (§2.3) — cheap, unblocks anyone else opening the repo.
   Done except for the items in §2.3.1 blocked on merging `add-v1-smth` to
   `main`.
2. Measurement gate (§2.2) — per spec, required before investing further
   in more levers; also the only way to know if §2.1 is worth building at
   all. Harness is built; what's left is running it for real (see §2.2's
   "still not started" list) and adding tasks beyond `fix-typo`.
3. Context lifecycle hooks (§2.1) — only after the gate exists to judge
   them against.
