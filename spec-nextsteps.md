# Winglet Codex Parity Next Steps

This spec covers the two remaining Codex parity gaps found during the Claude
Code versus Codex capability review.

## 1. Install Codex Subagent Hook Events

### Problem

`cmd/codex-hook` already handles `SubagentStart` and `SubagentStop` as
investigation signals, but `install.sh` does not register those events in
`${CODEX_HOME:-~/.codex}/hooks.json` or `.codex/hooks.json`.

The result is that unit tests cover subagent behavior, but installed users do
not get it.

### Required Change

Update Codex hook installation and removal wiring so the Codex hook is registered
for:

- `SubagentStart`
- `SubagentStop`

Use the same command hook entry shape and timeout as the existing Codex events.
The merge must remain idempotent and must not overwrite unrelated hook entries.

### Acceptance Criteria

- `./install.sh --hook-only --codex-only` adds the Codex hook to
  `hooks.SubagentStart` and `hooks.SubagentStop`.
- Re-running install does not duplicate either entry.
- `./install.sh --local --hook-only --codex-only` writes the same event coverage
  to `.codex/hooks.json`.
- `./uninstall.sh --hook-only --codex-only` removes the Codex hook from those
  events while preserving unrelated hooks.
- Installer smoke tests assert both events are present.

### Suggested Tests

- Extend `scripts/smoke-install-hooks.sh` to check global and/or local Codex hook
  config includes `SubagentStart` and `SubagentStop`.
- Add uninstall assertions if the smoke test already verifies removal symmetry.

## 2. Broaden Codex Non-Shell Tool Coverage

### Problem

Claude parity is strongest for shell output, but Claude currently handles richer
tool outputs:

- `Grep` content budgeting
- `Glob` filename budgeting
- broad investigate-output retirement for read/search-style tools

Codex currently rewrites only shell-like output and uses `apply_patch` only as an
implementation-phase signal. Codex hooks can observe more local function tools,
including `apply_patch`, MCP tools, and other function tools, so Winglet can cover
more than shell.

### Scope

Start with deterministic, schema-backed local tools only. Do not guess opaque
schemas in a way that could corrupt model-visible output.

Phase 1 should cover:

- `apply_patch` as an implementation signal only, preserving current behavior.
- Shell-like tools exactly as today.
- Local function tools whose `tool_response` is a model-facing string.
- Local function tools whose `tool_response` has a clear text field, limited to
  `output`, `content`, or nested `result.output` / `result.content`.

Defer MCP-specific structured rewrites unless real payload fixtures show a safe
and reversible shape.

### Required Change

Introduce a Codex tool-output normalization layer that can classify:

- command identity for ledger keys
- whether the output is safe to rewrite
- whether the tool call is investigate-like, implement-like, or neutral
- the text to budget, dedup, or retire

The replacement path must still use Codex-supported `PostToolUse` feedback:

- `continue: false`
- `stopReason`
- `systemMessage`

The hook must archive full original output before any budget or retirement
replacement.

### Classification Rules

Keep the current conservative shell command classifier.

For non-shell local function tools:

- Treat read-only filesystem-style names as investigate-like when their names
  clearly indicate reading or searching, for example `read_file`, `list_dir`,
  `grep`, `search`, `find`, or `glob`.
- Treat edit/write/patch names as implement-like, for example `apply_patch`,
  `edit`, `write_file`, or `replace`.
- Leave unknown names neutral.

When in doubt, preserve output unchanged.

### Acceptance Criteria

- Existing Codex shell behavior remains unchanged.
- Repeated safe string outputs from supported non-shell local tools are deduped.
- Long safe string outputs from supported non-shell local tools are budgeted
  pre-boundary.
- Supported investigate-like non-shell outputs are retired after the
  investigate-to-implement boundary or after the investigation threshold.
- Unknown structured responses pass through untouched.
- Failed, interrupted, image, or error-bearing outputs pass through untouched.
- Stats are still tagged with `stats.AgentCodex`.

### Suggested Tests

- Add table-driven tests for `codexModelVisibleOutput` or its replacement
  normalization layer:
  - raw string output
  - `{ "output": "..." }`
  - `{ "content": "..." }`
  - `{ "result": { "output": "..." } }`
  - failed/error/interrupted/image cases
  - unknown structured object
- Add `PostToolUse` tests for a synthetic non-shell local read tool:
  - first call passes or budgets depending on size
  - repeat call dedups
  - post-boundary call retires
- Add regression tests proving `apply_patch` still emits only the compact nudge
  and does not attempt to rewrite patch output.

## Non-Goals

- No LLM summarization.
- No automatic compaction trigger.
- No retroactive rewrite of already-delivered history.
- No broad MCP output rewriting without real fixtures.
- No changes to dashboard metrics beyond counting the new suppressions through
  existing stats fields.

## Verification

Run:

```sh
go test ./...
make installer-smoke
```

Manual dogfood check:

1. Install Codex hooks locally with `./install.sh --local --hook-only --codex-only`.
2. Inspect `.codex/hooks.json` and trust hooks via `/hooks`.
3. Run a Codex session that starts a subagent, then performs an edit.
4. Confirm the compact nudge appears after the subagent-to-implementation
   boundary.
5. Run repeated and long safe local-tool outputs and confirm receipts replace
   original output.
