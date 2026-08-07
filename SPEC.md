# Winglet on Codex - SPEC

Implementation spec for adding OpenAI Codex support to Winglet while keeping
the existing Claude Code support intact.

This document is based on the current repo, the local Codex CLI
`0.146.0-alpha.9.2`, the official Codex manual fetched on 2026-08-07, and a
schema-only look at local `~/.codex/sessions/**/rollout-*.jsonl` files. Codex
explicitly says transcript files are a convenience, not a stable hook
interface, so rollout parsing must be fixture-backed and fail soft.

## Goal

Winglet should collect and display the same classes of savings for Codex
sessions that it currently collects for Claude Code sessions:

- Repeat shell output deduplication.
- Long output budgeting with a recoverable full-output archive.
- Investigation-output retirement after the session shifts into implementation.
- Compact guidance when useful.
- Session and project stats in the existing desktop app.
- A session-end savings receipt when receipts are enabled.

Codex is a second producer into the same Winglet state model. Do not replace
Claude Code support.

## Current Winglet Shape

The current Claude hook is `cmd/claude-hook`, renamed from the older generic
`cmd/ledger-hook` path.

- `cmd/claude-hook/main.go` implements Claude Code hooks for `SessionStart`,
  `PostCompact`, `PostToolUse`, `Stop`, and `SessionEnd`.
- `internal/ledger` stores same-session output hashes by `(project root,
  session_id)`.
- `internal/phase` tracks the investigate-to-implement transition and the
  pre-boundary investigate-call count.
- `internal/retire` content-addresses archived output under the per-session
  state directory.
- `internal/stats` persists one `*.stats.json` file per session and computes
  rollups by summing session files. It currently assumes a Claude transcript
  parser.
- `internal/transcript` parses Claude Code JSONL transcripts.
- `internal/pricing` prices Claude model input/cache-write tokens.
- `internal/statedir`, `internal/projectroot`, `internal/registry`, and
  `internal/config` are already mostly agent-neutral.
- `cmd/agent-winglet-app` reads JSON files directly. There is no daemon.

## Codex Facts To Build Against

Verified from the official Codex manual:

- Hooks are enabled by default. Disable with `[features].hooks = false`.
  `codex_hooks` is a deprecated alias, not the canonical feature key.
- Codex loads hooks from `hooks.json` or inline `[hooks]` tables next to active
  config layers, including `~/.codex/hooks.json`, `~/.codex/config.toml`, and
  trusted project `.codex/` layers.
- Matching hooks from multiple files all run. Higher-precedence config layers
  do not replace lower-precedence hooks.
- Non-managed command hooks must be trusted in `/hooks` before they run. The
  trust is tied to the current hook definition hash.
- Command hooks receive JSON on stdin with common fields including
  `session_id`, `transcript_path`, `cwd`, `hook_event_name`, and `model`.
- `SessionStart`, `PreToolUse`, `PermissionRequest`, `PostToolUse`,
  `UserPromptSubmit`, `SubagentStart`, `SubagentStop`, and `Stop` also include
  `permission_mode`.
- Tool hooks cover shell/unified-exec calls as `Bash`, `apply_patch` as
  `apply_patch` with `Edit`/`Write` matcher aliases, MCP tools by MCP name, and
  other local function tools. Hosted tools such as `WebSearch` are not covered.
- `PostToolUse` can replace the model-visible result by returning
  `decision: "block"` with `reason`, or by returning `continue: false` with
  hook feedback. A `decision: "block"` rejects nested code-mode tool promises;
  `continue: false` does not. Validate the exact non-code-mode UX before
  choosing the default replacement shape.
- `SessionEnd` output is advisory. It will not steer Codex or keep the thread
  open.
- `SessionStart`, `PreCompact`, `PostCompact`, `UserPromptSubmit`,
  `SubagentStop`, and `Stop` support shared `systemMessage`; `PostToolUse`
  also supports `systemMessage`.
- `PreCompact` and `PostCompact` match `trigger` values `manual` and `auto`.
- Codex stores local state under `CODEX_HOME`, defaulting to `~/.codex`.
- Session transcripts live under `$CODEX_HOME/sessions` unless a run is
  ephemeral. The hook also receives `transcript_path` directly.
- Codex warns that transcript format is not a stable hook interface.

Observed locally, but not a public stability contract:

- Rollout token counts currently appear as `event_msg` rows with
  `payload.type == "token_count"` and cumulative totals at
  `payload.info.total_token_usage`.
- Response items currently include `function_call`, `function_call_output`,
  `message`, and `reasoning` payload types.
- Function call output text is currently at `payload.output`.

## Architecture

Add a Codex hook binary and a small set of support packages. Keep the Claude
hook working as it does today.

```text
cmd/
  claude-hook/          # renamed from cmd/ledger-hook, behavior unchanged
  codex-hook/           # new Codex hook binary

internal/
  codexrollout/         # new Codex transcript/rollout parser
  cmdclass/             # new conservative shell command classifier
  ledger/               # reused
  phase/                # reused
  retire/               # reused
  stats/                # add Agent and agent-neutral transcript usage input
  pricing/              # add OpenAI model rates and provider-aware fallback
  registry/             # add Codex install detection helpers
```

Use separate binaries, not one binary with two modes. Claude Code and Codex have
different hook payloads and output semantics; sharing a `main` package would add
conditionals in the highest-risk code path.

## Data Model

### `stats.Session.Agent`

Add an agent marker so mixed history is explainable in the app.

```go
type Session struct {
    Agent string `json:"agent"` // "claude-code" or "codex"
    // existing fields...
}
```

Rules:

- Existing stats files without `agent` are treated as `claude-code`.
- Claude hook writes `claude-code` when it creates or updates a session file.
- Codex hook writes `codex`.
- Rollups stay aggregate-only. `Overview` and `ProjectRow` do not split totals
  by agent.
- `SessionRow` gains `Agent string`.

### Transcript Usage Interface

Do not make `internal/stats` import both transcript parsers. Instead, define a
small shared value type or keep using `transcript.SessionUsage` as the value
shape and let each hook adapt into it. The fields remain:

- `Tokens`
- `CostUSD`
- `ContentBytes`

The Claude parser remains in `internal/transcript`. Codex parsing belongs in
`internal/codexrollout`.

### Pricing

Extend `internal/pricing` for provider-aware fallback.

Recommended shape:

```go
func Lookup(model string, fallback string) Rate
```

or:

```go
func LookupClaude(model string) Rate
func LookupOpenAI(model string) Rate
```

Do not let an unknown Codex/OpenAI model fall back to a Claude model rate.
Populate OpenAI rates from official pricing at implementation time and include
tests for known model strings seen in local Codex rollout fixtures. Do not
hardcode prices from this spec.

## Codex Rollout Parser

Create `internal/codexrollout` with the same operational behavior as
`internal/transcript`:

- Stream JSONL line by line.
- Missing, unreadable, malformed, or partially-written files return zero usage
  and nil error.
- Provide whole-file and offset-based reads so `Stop` can update live stats
  without reparsing the full transcript.
- Do not consume an incomplete trailing JSONL line in offset mode.

Token accounting:

- Use the latest cumulative `token_count` event when present.
- Current local shape is:

```text
type == "event_msg"
payload.type == "token_count"
payload.info.total_token_usage.input_tokens
payload.info.total_token_usage.cached_input_tokens
payload.info.total_token_usage.cache_write_input_tokens
payload.info.total_token_usage.output_tokens
payload.info.total_token_usage.reasoning_output_tokens
payload.info.total_token_usage.total_tokens
```

- Count input-side content tokens only, matching the intent of
  `internal/transcript`: include `input_tokens` and `cache_write_input_tokens`;
  exclude `cached_input_tokens`, `output_tokens`, and
  `reasoning_output_tokens`.
- Price those included tokens with OpenAI input/cache-write rates when those
  distinctions are available. If Codex's rollout gives only aggregate
  `cache_write_input_tokens`, use the closest published cache-write rate and
  document the choice in `internal/pricing`.

Content bytes:

- Count user-authored message text once.
- Count `function_call_output` text once, because that is the closest local
  analog to tool output fed back into the model.
- Do not count assistant output, reasoning, encrypted content, or repeated
  cache reads.
- Add fixtures from real local rollout files with content redacted but schema
  preserved. The parser tests should cover both the current observed
  `payload.info.total_token_usage` shape and a missing-token-count fallback.

Validation gate:

- Before surfacing Codex percentages in the app, compare parser output against
  several real Codex sessions. Confirm `ContentBytes` is the same order of
  magnitude as the bytes of user text plus function-call output in the rollout.
  If not, display Codex suppressed bytes live but withhold percent/tokens/dollar
  estimates for Codex sessions until the denominator is trustworthy.

## Hook Output Replacement

Codex does not have Claude Code's `updatedToolOutput` field. Winglet has to
replace a whole model-visible tool result.

Build and dogfood a tiny replacement probe before porting suppression:

- For a harmless Bash command, return `decision: "block"` with a Winglet-style
  receipt.
- For the same command, return `continue: false` with the same receipt in the
  field Codex accepts as feedback.
- Observe both in a normal model tool call and in code-mode nested tool calls.
- Choose the shape that replaces output without causing retries, failure
  handling, or rejected nested promises.

Expected default if validation passes:

- Prefer `continue: false` if it gives the model the receipt and avoids
  code-mode promise rejection.
- Fall back to `decision: "block"` only if `continue: false` cannot carry the
  replacement text reliably.

This is a hard gate for dedup, budgeting, and retirement.

## Command Classification

Claude has distinct `Read`, `Grep`, `Glob`, `WebFetch`, `WebSearch`, `Task`,
`Edit`, `Write`, and `NotebookEdit` tools. Codex funnels most local inspection
through `Bash` or unified exec, so phase detection needs command
classification.

Create `internal/cmdclass`:

```go
type Class int

const (
    Neutral Class = iota
    Investigate
    Implement
)

func Classify(command string) Class
```

Conservative rules:

- Investigate: simple read-only commands with no shell control operators:
  `cat`, `less`, `head`, `tail`, `grep`, `rg`, `find`, `fd`, `ls`, `wc`,
  `git status`, `git diff`, `git log`, `git show`, `git grep`, `go test` in
  list/discover mode only if that can be detected safely, `curl`, and `wget`.
- Implement: no Bash commands in v1.
- Implement tool: `apply_patch`, matched as `apply_patch`, `Edit`, or `Write`.
- Neutral: empty commands, compounds with `&&`, `||`, `;`, pipes, redirects,
  command substitution, unknown prefixes, package managers, `git commit`,
  `sed -i`, `perl -i`, `mv`, `cp`, `rm`, and all MCP tools.

A false implement classification is worse than a miss because it can retire
output before the model has enough context. Bias toward `Neutral`.

## Mechanism Mapping

| Mechanism | Claude Code | Codex |
| --- | --- | --- |
| Project registration | `SessionStart`/`PostCompact` registers `projectroot.Resolve(cwd)` | Same on `SessionStart`, including `source == "compact"` |
| Transcript stats | `internal/transcript` on `PostToolUse`, `Stop`, `SessionEnd` | `internal/codexrollout` on `PostToolUse`, `Stop`, `SessionEnd` |
| Repeat shell output | `PostToolUse` `Bash`, key `Bash:<command>`, hash stdout | `PostToolUse` `Bash`, key `Bash:<command>`, hash extracted model-visible output |
| Output budgeting | Bash stdout, Grep content, Glob filenames | Bash/unified-exec output only |
| Phase boundary | Tool-name sets | `cmdclass` investigate plus `apply_patch` implement |
| Retirement | Retire investigate tools after boundary or threshold; retire long Bash output post-boundary | Retire classified investigate Bash output after boundary or threshold; retire long Bash output post-boundary |
| Compact nudge | `PostToolUse` message and additional context | Build only after validating Codex auto-compact behavior and output UX |
| Savings receipt | `SessionEnd` `systemMessage` | `SessionEnd` `systemMessage` |
| Subagents | Claude `Task` tool counts as investigation | Codex `SubagentStart`/`SubagentStop` should count as investigation, but do not try to retire subagent transcript output in v1 |

## Codex Hook Binary

`cmd/codex-hook/main.go` should mirror the structure of `cmd/claude-hook`:

- Package doc comment describes the behavior and non-goals.
- `run()` reads stdin and writes optional JSON stdout.
- `handle()` dispatches by `hook_event_name`.
- State writes use the same load/mutate/save pattern as the Claude hook.
- All transcript parsing failures are fail-soft.
- All stderr prefixes use `codex-hook:`.

Input struct:

```go
type hookInput struct {
    SessionID      string          `json:"session_id"`
    TranscriptPath string          `json:"transcript_path"`
    Cwd            string          `json:"cwd"`
    HookEventName  string          `json:"hook_event_name"`
    Model          string          `json:"model"`
    PermissionMode string          `json:"permission_mode"`
    TurnID         string          `json:"turn_id"`
    Source         string          `json:"source"`
    Trigger        string          `json:"trigger"`
    Reason         string          `json:"reason"`
    ToolName       string          `json:"tool_name"`
    ToolUseID      string          `json:"tool_use_id"`
    ToolInput      json.RawMessage `json:"tool_input"`
    ToolResponse   json.RawMessage `json:"tool_response"`
    AgentID        string          `json:"agent_id"`
    AgentType      string          `json:"agent_type"`
}
```

Tool input/output extraction:

- For Bash, extract `tool_input.command`.
- Capture real `PostToolUse` payloads before implementing output extraction.
- Implement an extractor returning `{Text string, Successful bool, Known bool}`.
- Only dedup/budget/retire when text is non-empty and success is known.
- If success cannot be determined from Codex's hook payload, start with stats
  and receipts only; do not guess from arbitrary strings unless tests prove the
  result format is stable enough.

## Install And Uninstall

The existing hook has been renamed before adding Codex:

- `cmd/ledger-hook` has become `cmd/claude-hook`.
- Installed binary is now `claude-hook`.
- README, Makefile, install/uninstall scripts, registry helpers, tests, and doc
  comments now use the new name.
- No pre-v1 `ledger-hook` compatibility path is kept.

Add `codex-hook` installation:

- `make claude-hook` builds `bin/claude-hook`.
- `make codex-hook` builds `bin/codex-hook`.
- `make build` can build both hooks.
- `install.sh --hook-only` installs both agent hooks by default.
- Add `--claude-only` and `--codex-only` selectors that compose with
  `--hook-only` and `--app-only`.
- Claude global target remains `~/.claude/settings.json`.
- Codex global target is `${CODEX_HOME:-$HOME/.codex}/hooks.json`.
- Codex local target for `--local --codex-only` is `./.codex/hooks.json`.
- Do not write `~/.codex/config.toml` just to enable hooks; hooks are enabled
  by default. If the user disabled `[features].hooks = false`, print a warning
  rather than rewriting their preference.
- After Codex install, print: `Run /hooks in Codex and trust the agent-winglet
  codex-hook before Winglet can record Codex sessions.`
- `uninstall.sh` removes `claude-hook` and `codex-hook` entries by basename.
- `--purge-data` continues deleting `~/.agent-winglet`; no Codex transcript
  files are deleted.

Use `jq` for JSON merges/removals, as the current scripts do. Avoid TOML
rewrites in shell.

## Desktop App

Keep the dashboard one product, not two dashboards.

Required mixed-agent changes:

- `SessionRow` gains `Agent string`.
- Session rows show a small muted badge: `Claude` or `Codex`.
- Existing aggregate Overview and Project rows continue summing all sessions.
- Project install detection should become agent-aware:
  - Claude installed if global or project `.claude/settings.json` contains
    `claude-hook`.
  - Codex installed if global or project `.codex/hooks.json` contains
    `codex-hook`.
  - Trust status may not be reliably visible. If it is not visible, do not
    invent a trusted/untrusted badge; rely on install output and docs.

Design constraints:

- No second accent color for Codex.
- No new top-level screen.
- No colored left-edge markers in repeated rows.
- Use the existing muted pill/card-sub visual language for the session badge.

Required card-label polish from the existing draft:

- Move the estimate marker out of the card labels and next to the rendered
  value.
- `Bytes saved` stays an unqualified measured value.
- `Tokens saved` and `Money saved` are plain labels.
- The value line renders `(est.)` after the value only when the value is real,
  not when it is `no data yet`.

Backend shape:

```go
type Card struct {
    Label     string `json:"label"`
    Tooltip   string `json:"tooltip"`
    Detail    string `json:"detail"`
    Sub       string `json:"sub"`
    Estimated bool   `json:"estimated"`
}
```

`buildOverview` should set:

- `BytesSavedCard.Label = "Bytes saved"` and `Estimated = false`.
- `TokensSavedCard.Label = "Tokens saved"` and `Estimated = true`.
- `DollarSavedCard.Label = "Money saved"` and `Estimated = true`.

Frontend shape:

```html
<div class="card-detail">
  ${c.detail}${c.estimated && c.detail !== 'no data yet'
    ? ' <span class="card-detail-est">(est.)</span>'
    : ''}
</div>
```

CSS:

```css
.card-detail-est {
  font-family: var(--font-ui);
  font-size: 11px;
  font-weight: 500;
  color: var(--text-secondary);
  margin-left: 2px;
}
```

The generated Wails bindings under `frontend/wailsjs/go/models.ts` and
`frontend/wailsjs/go/main/App.d.ts` must be refreshed or updated as part of the
same UI phase so the frontend model shape matches Go.

## Implementation Plan

### Phase 0 - Rename Claude Hook

Complete.

- `cmd/ledger-hook` is now `cmd/claude-hook`.
- Installed binary is `claude-hook`.
- Go, shell scripts, README, Makefile, tests, app copy, and doc comments use
  the new name.
- No compatibility path is kept for the old `ledger-hook` name.
- Exit: `go build ./...` and `go test ./...` pass.

### Phase 1 - Agent Field And UI Badge

Complete.

- `stats.Session.Agent` now persists `claude-code` or `codex`.
- Missing/legacy `agent` values default to `claude-code` at the stats load and
  save boundary.
- `SessionRow` exposes `Agent` to the frontend.
- Session rows render a muted `Claude` or `Codex` badge.
- `Card.Estimated` drives `(est.)` beside real token/money values, while card
  labels stay plain.
- Wails TypeScript models are updated for the new fields.
- Exit: `go test ./...` and `npm run build` pass.

### Phase 2 - Codex Rollout Parser

Complete.

- `internal/codexrollout` streams Codex rollout JSONL into the shared
  `transcript.SessionUsage` shape.
- A redacted schema-preserving fixture covers the observed local rollout
  `response_item`, `function_call_output`, and cumulative `token_count` rows.
- Full reads reconcile the latest cumulative token totals and return the
  consumed byte offset.
- Offset reads skip incomplete trailing JSONL lines and return a true delta by
  subtracting the caller's previous usage baseline from Codex's cumulative
  token rows.
- Content-byte accounting counts user-role message text and
  `function_call_output` text, while assistant output, reasoning, cached-input
  replays, and malformed rows are ignored.
- `internal/pricing` now has provider-specific Claude and OpenAI lookups, with
  Codex/OpenAI unknown models falling back to an OpenAI rate rather than a
  Claude rate.
- Exit: `go test ./internal/codexrollout ./internal/pricing`,
  `go test ./...`, and `go build ./...` pass.

### Phase 3 - Minimal Codex Hook

Complete locally; real Codex trust/dogfood remains a manual validation step.

- `cmd/codex-hook` is a separate stats-only Codex hook binary.
- `SessionStart` and `PostCompact` register the project and reset
  session-scoped ledger, phase, retire, and stats state.
- `PostToolUse` and `Stop` record rollout usage deltas through
  `internal/codexrollout`.
- `SessionEnd` reconciles the full rollout usage, writes Codex-tagged session
  stats, and emits a receipt only if suppression activity exists.
- No Codex tool output is replaced in this phase.
- `make build` now builds `bin/claude-hook` and `bin/codex-hook`; direct
  `make claude-hook` and `make codex-hook` targets exist.
- `install.sh` installs both hooks by default, supports `--claude-only` and
  `--codex-only`, writes Codex hooks to
  `${CODEX_HOME:-$HOME/.codex}/hooks.json` globally or `./.codex/hooks.json`
  with `--local`, and prints the `/hooks` trust reminder.
- `uninstall.sh` removes both hook entries by basename and supports the same
  hook selectors.
- Project install detection treats either Claude or Codex hook config as
  installed.
- Exit: `bash -n install.sh`, `bash -n uninstall.sh`,
  `go test ./cmd/codex-hook ./internal/registry`, `go test ./...`,
  `go build ./...`, `make build`, temp Codex-only install/uninstall smoke,
  global `./install.sh --hook-only --codex-only`, and `git diff --check`
  pass. A real Codex session still needs `/hooks` trust before it can validate
  a Codex-tagged row end to end.

### Phase 4 - Replacement Probe

Probe harness complete locally; real Codex dogfood remains the hard gate before
any suppression mechanism is enabled.

- `cmd/codex-hook` now has a disabled-by-default `PostToolUse` replacement
  probe guarded by `AGENT_WINGLET_CODEX_PROBE`.
- The probe only fires for `Bash` commands whose `tool_input.command` contains
  `agent-winglet-codex-probe`, so accidental normal sessions stay stats-only.
- `AGENT_WINGLET_CODEX_PROBE=continue`, `continue-false`, `true`, or `1`
  returns `continue: false` with `stopReason` and `systemMessage` set to the
  probe receipt.
- `AGENT_WINGLET_CODEX_PROBE=block` or `decision-block` returns
  `decision: "block"` with `reason` set to the probe receipt and matching
  `hookSpecificOutput.additionalContext`.
- Unit tests cover disabled default behavior, non-matching commands, and both
  JSON output shapes.
- Exit so far: `go test ./cmd/codex-hook` passes.

Manual dogfood still required before enabling stronger suppression paths:

1. Reinstall the updated global Codex hook binary.
2. Run `/hooks` in Codex and trust the updated `codex-hook` definition.
3. In a normal Codex tool call, run a harmless command containing
   `agent-winglet-codex-probe` once with `AGENT_WINGLET_CODEX_PROBE=block` and
   once with `AGENT_WINGLET_CODEX_PROBE=continue`.
4. Repeat the same two modes through a code-mode nested Bash/unified-exec call.
5. Record any divergence from the selected replacement shape here.

Selected replacement shape for Phase 6 is `continue: false` with `stopReason`
and `systemMessage`. The current Codex manual says `PostToolUse`
`continue: false` replaces the model-visible tool result and does not reject
nested code-mode tool promises, which is the desired behavior for dedup
receipts. Fall back to `decision: "block"` only if dogfood shows
`continue: false` cannot carry the replacement receipt reliably.

### Phase 5 - Command Classifier

Complete.

- `internal/cmdclass` exposes the planned `Class`, `Neutral`,
  `Investigate`, `Implement`, and `Classify(command string)` API.
- It classifies only simple known read-only Bash commands as `Investigate`.
- Shell control operators, redirections, command substitution, unknown
  prefixes, package managers, mutating git/package/file commands, mutating
  `find`, writing `curl`/`wget` forms, MCP-looking commands, and non-list
  `go test` commands stay `Neutral`.
- Bash command classification never returns `Implement` in v1; `apply_patch`,
  `Edit`, and `Write` remain future non-Bash tool signals.
- Exit: `go test ./internal/cmdclass` passes.

### Phase 6 - Dedup

Complete locally; real Codex dogfood remains before treating the exit as fully
closed.

- `cmd/codex-hook` now uses `internal/ledger` for Codex `Bash` `PostToolUse`
  repeat detection.
- The dedup key is `Bash:<command>`, matching the Claude hook's Bash ledger
  key shape.
- Repeated output is replaced with a compact receipt through the selected
  `continue: false` / `stopReason` / `systemMessage` shape.
- The extractor accepts plain model-facing string output plus structured
  `stdout`, `output`, `content`, and nested `result` payloads.
- The extractor skips empty output, stderr/error output, interrupted/image
  output, explicit nonzero exit codes, `success: false`, and failed statuses.
- Repeat hits record `DedupHits`, `DedupBytes`, and `AgentCodex` in the
  existing stats session file.
- Exit so far: `go test ./cmd/codex-hook` passes.
- Remaining dogfood exit: a real Codex session shows repeated command output
  replaced by a receipt and reflected in the dashboard.

### Phase 7 - Output Budgeting

Complete locally; real Codex dogfood remains before treating the exit as fully
closed.

- Shared head/tail budgeting now lives in `internal/outputbudget`.
- `cmd/claude-hook` calls the shared package through thin compatibility
  wrappers, preserving the existing Claude hook behavior while making the
  implementation reusable.
- `cmd/codex-hook` applies budgeting after a successful shell output misses the
  dedup ledger, so repeat detection still wins over trimming.
- Codex shell support accepts `Bash` plus conservative unified-exec-style tool
  names and both `tool_input.command` and `tool_input.cmd`.
- Full first-time long output is archived through `internal/retire.Store` before
  the model-visible output is replaced with the head/tail receipt.
- Budget hits record `BudgetTrims`, `BudgetLinesOmitted`,
  `BudgetBytesOmitted`, and `AgentCodex`.
- Unit tests cover Codex Bash budgeting, archive recovery, stats updates,
  unified-exec-shaped output, and dedup-over-budget precedence.
- Exit so far: `go test ./cmd/codex-hook ./cmd/claude-hook
  ./internal/outputbudget` passes.
- Remaining dogfood exit: a real Codex session trims a long command, the archive
  path exists, and the dashboard updates live.

### Phase 8 - Phase And Retirement

1. Wire `cmdclass.Investigate` and `apply_patch` implement signals through
   `internal/phase`.
2. Preserve `investigateCallThreshold`.
3. Retire later investigate-classified Bash output after boundary or threshold.
4. Retire long first-time Bash output post-boundary as Claude does.
5. Count Codex subagent start/stop as investigation, but do not retire subagent
   transcripts in v1.
6. Exit: dogfood sessions cover post-boundary retire and threshold retire.

### Phase 9 - Compact Guidance

1. Validate whether Codex auto-compaction makes the existing nudge useful.
2. If Codex auto-compacts well enough, do not implement the nudge for Codex.
3. If still useful, emit a one-time `systemMessage` on the boundary using a
   Codex-supported event/output shape.
4. Do not instruct Codex to ask the user through a Claude-specific tool name.
5. Exit: the nudge appears once and can be disabled by the existing
   `CompactNudgeDisabled` config.

### Phase 10 - End-To-End Install

1. Fresh install writes Claude and Codex hook config.
2. `--claude-only`, `--codex-only`, `--hook-only`, `--app-only`, and `--local`
   combinations behave predictably.
3. Uninstall removes current hook entries.
4. README explains Codex trust through `/hooks`.
5. Exit: `go build ./...`, `go test ./...`, installer smoke tests, and one real
   Codex dogfood session pass.

## Non-Goals

- No single unified hook binary.
- No MCP classification in v1.
- No modification or deletion of Codex session transcript files.
- No attempt to bypass Codex hook trust.
- No project-level Codex hook install unless `--local` is explicit.
- No new dashboard screen or separate Codex theme.
- No cost-savings claims stronger than the current app's estimate framing.
- No Windows/Linux manual Codex validation beyond existing app build smoke
  tests unless a later implementation task explicitly asks for it.

## Open Questions

- Which Codex `PostToolUse` replacement shape is best for Winglet:
  `continue: false` or `decision: "block"`?
- What exact Bash `tool_response` shape does Codex pass to hooks on this CLI
  version, and does it expose success/stderr/interruption structurally?
- Is Codex auto-compaction good enough to skip the compact nudge?
- Where, if anywhere, does Codex persist hook trust state in a supported enough
  way for the app to display "installed but not trusted"?
- Which OpenAI model strings should be considered first-class in
  `internal/pricing` for the Codex surfaces this repo actually uses?
