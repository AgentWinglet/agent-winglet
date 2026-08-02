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

**Budget output by outcome — done.** `cmd/ledger-hook/main.go`'s
`budgetStdout` now collapses a first-time (non-repeat), successful Bash
command's stdout to its first 15 + last 15 lines once it exceeds 60 lines,
with an `[agent-winglet] N lines omitted, exit 0 (...)` marker in between.
"Successful" uses the same proxy the repeat-check already relies on (empty
stderr, not interrupted, not an image) since the Bash `tool_response`
schema has no exit-code field exposed to hooks. The repeat-check still
runs first and takes precedence: an exact repeat gets the `unchanged since
turn N` message even if it would also qualify for budgeting. The ledger
hashes the full, un-budgeted stdout, so a later exact repeat of a budgeted
command is still detected correctly. Covered by `go test ./...`: threshold
boundary (just under/at/just over), stderr/interrupted/image skip cases,
and repeat-check precedence. Not yet verified against a real `claude -p`
session (same caveat as the rest of §1 — mechanically works as designed,
not yet validated live).

Three of the four levers are still not started:

- **Retire used-up context at phase boundaries.** No mechanism yet to
  detect a phase boundary (e.g. "investigation done, implementation
  starting") or to replace a stale file-read/test-log with a compact
  receipt once it's served its purpose. Distinct from the Ledger's
  exact-repeat case — this is for content read once, used, and now stale.
- **Trim tool/schema definitions to task phase.** No mechanism to defer
  tool schemas the current phase won't call.
- **Compact at investigate→implement boundary.** No hook exists yet that
  triggers or suggests compaction at a natural phase transition rather
  than at the context-window cliff.

Each of these needs its own design pass — they weren't scoped or
prototyped alongside the Ledger.

### 2.2 Measurement gate (spec §5) — deferred, set aside for now

This is the actual gate before the tool is anything more than a personal
toy, per the full spec's Phase 2 rule. Not skipped — deliberately set
aside until there's a bigger footprint worth measuring. See
`~/agent-winglet-v1-spec.md` §5's 2026-08-03 note for the full reasoning;
summary below.

**What happened:** a paired-run harness (`internal/harness`, `cmd/measure`,
`cmd/usage-per-solve`, `harness/`) was built, and — after fixing a real bug
where `claude -p` silently denied every trial because project
`settings.json` `permissions.allow` is ignored in headless mode (needs the
`--allowedTools` CLI flag instead) — run for real against `claude -p` on
the one example task (`fix-typo`: fix a typo, then re-run `go build ./...`
verbatim to give the Ledger a repeat to catch).

Result: 9 real paired trials, 9/9 success both variants, **identical
`usage_per_solve` between hook and control** ($0.1708 either way). Traced
to the root cause, not just observed: `go build ./...` prints nothing on
success, and the Session Ledger hook explicitly skips empty stdout
(`if response.Stdout == "" { return nil, nil }` in
`cmd/ledger-hook/main.go`) — confirmed by inspecting a real trial's ledger
state file, which had zero entries for the `go build` command. The task
never gave the hook anything to act on, regardless of repeat count. This
isn't a hook bug (skipping empty output is correct — there's nothing to
compact) — it's a task-design gap: small single-file fixup tasks tend not
to produce the kind of repeated, non-trivial output the Ledger is meant to
compact.

**Decision:** rather than keep tuning toy tasks to force a signal, the
harness and its code have been removed from the working tree (still
recoverable from git history — commit `afa445a` on `add-v1-smth` — nothing
is lost, just parked). Revisit once §4.2's context-lifecycle levers give
the tool a bigger real footprint to measure against; a small hooks-only
Ledger on trivial tasks isn't where the interesting signal will be.

**Still true regardless of when this resumes:** `total_cost_usd` from
`claude -p` is Anthropic's computed cost, the closest scriptable proxy to
usage — but the spec asks for measurement against actual weekly-cap
consumption, which isn't exposable per-invocation. Any future harness
needs a batch of real trials cross-checked against actual weekly-cap
percentage in the Claude Code UI before the gate counts as closed, and
needs more than one task/scenario to say anything general.

**Nothing in §1 should be treated as validated —** only as "mechanically
works as designed." That remains true; the harness that would validate it
is parked, not built out further, for now.

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
2. Context lifecycle hooks (§2.1) — build enough of these to give the tool
   a real footprint worth measuring.
3. Measurement gate (§2.2) — resume once §2.1 exists. The harness code is
   parked in git history (commit `afa445a`), not gone; reviving it is
   cheaper than the original build was.
