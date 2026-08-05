# Next steps — buildable bits

Follow-up to [AGENTDIET_COMPARISON.md](AGENTDIET_COMPARISON.md). That doc
found agent-winglet's closer relatives aren't AgentDiet but two papers that
independently validate the same non-LLM, deterministic approach:

- **"The Complexity Trap: Simple Observation Masking Is as Efficient as LLM
  Summarization for Agent Context Management"** (Lindenbauer et al.,
  JetBrains Research, DL4C @ NeurIPS 2025, arXiv:2508.21433) — deterministic
  masking of old tool observations halves cost vs. a raw agent while
  matching/beating LLM-summarization solve rate.
- **"The Missing Memory Hierarchy: Demand Paging for LLM Context Windows"**
  (Mason, 2026, arXiv:2603.09023) — a transparent proxy evicts stale content
  to storage, leaves a reference behind, up to 93% context reduction in
  production.

Everything below is scoped to what fits the existing hook architecture: a
`PostToolUse` hook's `updatedToolOutput` can only rewrite the tool call it is
currently processing, never an earlier one, and there is no hook to force
compaction or intercept the model mid-generation. That constraint (already
documented in `retire.go` and `phase.go`) rules out true page-fault-style
reinjection — it's noted below as out of scope, not forgotten.

## 1. Generalize output budgeting beyond Bash (do first)

**Status quo:** `budgetStdout` in
[cmd/ledger-hook/main.go:94-119](cmd/ledger-hook/main.go#L94-L119) only
truncates Bash `stdout` over 60 lines. Read/Grep/Glob/WebFetch/WebSearch get
no truncation at all pre-boundary — only Bash does today
([main.go:504](cmd/ledger-hook/main.go#L504) gates the whole budgeting branch
on `in.ToolName != "Bash"`).

**Why this is #1:** it's the most direct code translation of Complexity
Trap's actual finding — masking generalizes across tool types, not just
shell output — and it's the biggest coverage gap in the current mechanism
set. Grep/Glob matches and WebFetch/WebSearch payloads can be just as large
as Bash stdout and currently pass through untouched pre-boundary.

**Shape of the change:**
- Extract a tool-agnostic version of `budgetStdout` that takes an arbitrary
  string body instead of assuming a `bashOutput{Stdout, Stderr, ...}` shape.
- Add per-tool `tool_response` field extraction: Grep/Glob results, WebFetch
  content, WebSearch results each have their own JSON shape (needs
  confirming against real `PostToolUse` payloads, same way `bashOutput` was
  confirmed — don't guess the schema).
- Reuse the existing head/tail receipt format
  (`"[agent-winglet] %d lines omitted..."`) so the mechanism reads the same
  across tools.
- Extend `stats.Session` accounting (`BudgetTrims`, `BudgetLinesOmitted`,
  `BudgetBytesOmitted`) to cover non-Bash budget hits, or add
  tool-tagged counters if the savings receipt should break this out
  per-tool.
- Decide the interaction with retirement: post-boundary, investigate tools
  already get fully retired (`handleRetireInvestigate`,
  [main.go:416-443](cmd/ledger-hook/main.go#L416-L443)). Budgeting should
  only apply **pre-boundary**, where retirement doesn't fire yet — that's
  the actual coverage gap.

**Effort:** small — mostly plumbing, reusing an existing algorithm.

## 2. Token-based threshold instead of line count

**Status quo:** `budgetLineThreshold = 60`
([main.go:82](cmd/ledger-hook/main.go#L82)) is a line-count proxy — gameable
by output with unusually long or short lines, and not comparable to
AgentDiet's token-based `θ=500` trigger.

**Shape of the change:** swap the threshold check to a rough token estimate
(e.g. `len(content)/4` as a cheap proxy — no tokenizer dependency needed,
this doesn't have to be exact) applied uniformly across whichever tools #1
covers.

**Effort:** small, best done alongside #1 rather than as a separate PR.

## 3. Retire Bash post-boundary too

**Status quo:** post-boundary, investigate tools
(Read/Grep/Glob/WebFetch/WebSearch/Task) get fully archived via
`retire.Store` and replaced with a receipt. Bash post-boundary still only
gets the head/tail budget treatment (or ledger dedup) — it never gets
archived to disk.

**Shape of the change:** once `pastBoundary` is true
([main.go:493](cmd/ledger-hook/main.go#L493)), route long/first-time Bash
output through `retire.Store` the same way investigate tools are, instead of
just budgeting it. Requires deciding whether Bash should be reclassified as
investigate-like post-boundary, or whether retirement should be triggered
independent of the investigate/implement tool split.

**Effort:** medium — touches the phase/retire interaction, not just a new
code path.

## 4. Sliding-window masking pre-boundary

**Status quo:** nothing is touched pre-boundary except Bash budgeting/dedup.
Complexity Trap's masking strategy doesn't wait for a phase boundary at
all — it masks any observation older than N tool calls, unconditionally.

**Shape of the change:** add a call counter to `phase.State` (or a sibling
package) and mask/retire any investigate-classified output once it's more
than N calls old, regardless of phase. Mechanically simple — a counter plus
the existing `retire.Store`/receipt path — but this is a bigger philosophy
change than 1-3: agent-winglet currently only intervenes *after*
implementation starts (deliberate, per `phase.go`'s doc comment), and this
would make it intervene continuously from turn one.

**This one needs a product decision, not just an engineering one** — flag
before building: does always-on masking risk removing something the agent
still needs mid-investigation, in a way the current "only after the
boundary" design was specifically built to avoid?

**Effort:** medium engineering, but blocked on that decision first.

## 5. Already free — document it, don't build it

`retire.Store` already writes archived content to a real on-disk path, and
the receipt string already includes that path
([main.go:430-433](cmd/ledger-hook/main.go#L430-L433)). The agent can
already re-`Read` that path itself if it wants retired content back — that
*is* Pichay's page-fault-refetch behavior, just initiated by the model
issuing an ordinary tool call instead of a proxy intercepting one. Worth a
line in the README/docs confirming this is intentional and works today;
not a build item.

## Out of scope

**True demand-paging fault detection** (Pichay's proxy noticing the model is
about to reference evicted content and silently reinjecting it before the
model asks) is not buildable on this hook API. `PostToolUse` fires after a
tool call completes; there's no hook that observes model reasoning or
message drafting before it commits to a tool call, so "the model just tried
to recall something we removed" can't be detected the way a message-stream
proxy detects it. Same wall `retire.go`'s doc comment already names for
retroactive transcript rewrites — this is the same constraint, different
paper.

## Suggested order

1 → 2 (same PR, small) → 3 → decide on 4 → 5 (docs-only, anytime).
