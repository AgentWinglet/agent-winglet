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

### 2.1 Context lifecycle hooks (spec §4.2) — resolved (2 built, 2 out of hooks-only scope)

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

**Trim tool/schema definitions to task phase — resolved, out of v1 scope
(2026-08-03).** Second scope correction, same pattern as the Read finding
in §1. As of Claude Code v2.1.69, built-in tool schemas are deferred by
default behind `ToolSearch` — confirmed live in this session (the deferred-
tools list at session start, and the `tengu_deferred_stub_tool: true` flag
in `~/.claude.json`) and corroborated publicly
(github.com/anthropics/claude-code/issues/31002; platform.claude.com/docs
tool-search-tool page). Mechanism: `defer_loading: true` on tool
definitions sent to the Messages API — the model calls `ToolSearch` to pull
in a schema on demand, so tools never invoked in a session never cost
context. This is demand-driven, not phase-classified, so it's a stronger
version of what the spec's lever asked for (no heuristic needed to guess
the task phase).

More importantly, it's **not something a hooks-only project layer could
improve on even if it wanted to**: `defer_loading` is a Messages API
parameter set by the harness itself, invisible to and unconfigurable from
`.claude/settings.json`. `PreToolUse` — the only hook with any tool-call
visibility — fires after the model has already decided to call a tool
whose schema is already loaded; there's no hook event upstream of that
controls which schemas enter context in the first place. Unlike the Read
case (unnecessary to build), this one is inaccessible to a hooks-only
architecture, period. Retired from v1 scope, not just deferred.

**Compact at investigate→implement boundary — prototyped (2026-08-03).**
`internal/phase` gives the codebase its first concrete, detectable "phase
boundary" signal: `phase.State.Observe` classifies each `PostToolUse`
tool_name as investigate (`Read`/`Grep`/`Glob`/`WebFetch`/`WebSearch`/`Task`)
or implement (`Edit`/`Write`/`NotebookEdit`) — `Bash` is deliberately left
unclassified, since tool_name alone can't tell a read-only command from a
mutating one and a wrong guess either direction is worse than staying
neutral — and reports a crossing on the first implement call after at least
one investigate call this session. `cmd/ledger-hook/main.go`'s
`handlePhaseBoundary` wires this into the same binary and hook events the
Ledger already uses, latched to fire at most once per session (mirroring
`ledger.State`'s same-session-only lifetime, including reset on
`SessionStart`/`PostCompact` — verified: `TestHandlePhaseBoundaryResetsOn*`
in `cmd/ledger-hook/main_test.go`).

**Scope-narrowing finding made while building this:** confirmed against the
hooks reference (code.claude.com/docs/en/hooks, 2026-08-03) that no hook
event can *trigger* compaction programmatically — `PreCompact` only observes
or can block a compaction already under way; there is no hook-callable
"compact now." So this lever, in a hooks-only architecture, can only ever
*suggest*, never trigger, regardless of implementation effort. The hook does
so on both channels available: `systemMessage` (shown directly to the user)
and `hookSpecificOutput.additionalContext` (fed to the model, in case it
should act on the suggestion itself, e.g. by proposing `/compact` in its
next reply).

Required broadening the `PostToolUse` hook registration from `matcher:
"Bash"` to all tools (`.claude/settings.json`, `install.sh`'s jq merge) since
the investigate-classified tools are non-Bash. The dedup check in
`install.sh` no longer keys on `matcher == "Bash"`; a project that already
ran the old install.sh has a stale Bash-only entry that won't be upgraded by
re-running it — acceptable for now since nothing outside this repo's own dev
fixture has installed the hook yet (`add-v1-smth` isn't merged to `main`).

Covered by `go test ./...`: crossing fires only after a real investigate
call, never fires on an implement-first session, fires at most once, Bash
never counts either direction, and both `SessionStart`/`PostCompact` let it
fire again. Also smoke-tested by hand (`echo ... | ledger-hook`, matching
the pattern used to validate the Ledger): Read → silent, first Edit →
`systemMessage` + `additionalContext` both populated, second Edit → silent.
**Not yet verified against a real `claude -p` session** — same caveat as
the rest of §1/§2.1: mechanically works as designed, not yet observed
firing inside an actual Claude Code run.

**Retire used-up context at phase boundaries — resolved, out of v1 scope
(2026-08-03).** Third scope correction, same pattern as the Read finding in
§1 and the tool-schema finding above. Investigated whether `phase.Observe`'s
crossing signal (above) could drive this lever as literally specified: on
the investigate→implement crossing, replace the investigate-phase tool
outputs *already sent earlier in the transcript* with a compact receipt.

Confirmed against the hooks reference (code.claude.com/docs/en/hooks,
2026-08-03): a `PostToolUse` hook's `hookSpecificOutput.updatedToolOutput`
only replaces the result of the tool call *that invocation is currently
processing* — it has no way to reach back and rewrite the output of an
earlier tool call already sitting in the transcript. No other hook event or
output field can do this either: `MessageDisplay`'s `displayContent` is
explicitly display-only ("the transcript and what Claude sees keep the
original"), and `PreCompact` only observes or blocks a compaction already
under way (same finding the boundary-suggestion lever above relies on).
There is no hook-callable "rewrite turn N."

Same underlying cause as the tool-schema-trimming finding: this lever
assumes a capability — retroactively touching context already sent — that
sits upstream of every hook Claude Code exposes. `phase.Observe` correctly
identifies *when* investigate content becomes retirable, but no hook
mechanism can act on that signal against anything but the tool call
currently in flight. A hooks-only architecture can compact content at the
moment it's first produced (the output-budgeting lever, already built) but
not retroactively, once the model has already seen it in full.

Retired from v1 scope, not just deferred — same status as tool-schema
trimming. All four §4.2 levers are now resolved: two built (output
budgeting; investigate→implement compact suggestion), two confirmed
unbuildable in a hooks-only architecture (tool/schema trimming; retiring
used-up context).

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
2. Context lifecycle hooks (§2.1) — done. All four §4.2 levers resolved:
   output budgeting and the investigate→implement compact suggestion are
   built; tool/schema trimming and retiring used-up context are both
   confirmed unbuildable in a hooks-only architecture (native harness
   behavior in the first case, no hook can rewrite transcript history in
   the second).
3. Measurement gate (§2.2) — next up. The harness code is parked in git
   history (commit `afa445a`), not gone; reviving it is cheaper than the
   original build was.
