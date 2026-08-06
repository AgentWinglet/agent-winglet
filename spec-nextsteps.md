# Next steps — buildable bits

Follow-up to [AGENTDIET_COMPARISON.md](AGENTDIET_COMPARISON.md). That doc
found agent-winglet's closest relative isn't AgentDiet but a paper that
independently validates the same non-LLM, deterministic approach:

- **"The Complexity Trap: Simple Observation Masking Is as Efficient as LLM
  Summarization for Agent Context Management"** (Lindenbauer et al.,
  JetBrains Research, DL4C @ NeurIPS 2025, arXiv:2508.21433) — deterministic
  masking of old tool observations halves cost vs. a raw agent while
  matching/beating LLM-summarization solve rate.

Everything below is scoped to what fits the existing hook architecture: a
`PostToolUse` hook's `updatedToolOutput` can only rewrite the tool call it is
currently processing, never an earlier one, and there is no hook to force
compaction or intercept the model mid-generation. That constraint is already
documented in `retire.go` and `phase.go`.

## 1. Generalize output budgeting beyond Bash — done (Grep/Glob only)

Shipped for Grep and Glob. `budgetStdout` is now a thin Bash-specific
wrapper around a tool-agnostic `budgetBody` core (`cmd/ledger-hook/main.go`),
and `handlePostToolUse` dispatches pre-boundary budgeting to
`handleGrepBudget`/`handleGlobBudget` alongside the existing
`handleBashPostToolUse`. Schemas were confirmed rather than guessed:
`GrepOutput`/`GlobOutput` against the `sdk-tools.d.ts` shipped inside the
installed `@anthropic-ai/claude-code` npm package (no Grep/Glob tools were
available to trigger live in the dev session this was built in). Each
handler round-trips the full response struct — not just the budgeted
field — so nothing else the real tool_response carried gets dropped.

**Descoped, not forgotten:**
- **WebFetch** — considered and deliberately cut after implementing it,
  once its actual shape (confirmed live) sank in: `result` isn't raw page
  content, it's already the output of fetch → markdown → a small model
  answering the caller's specific prompt, plus the tool self-summarizes
  huge sources before we ever see it. That means it's rarely long enough to
  need budgeting, and on the rare occasions it is, it's because the prompt
  asked for something extensive — head/tail truncation would cut the
  requested content itself, not waste, unlike Bash's setup/noise/result
  shape or Grep/Glob's independent, interchangeable entries.
- **WebSearch** — its `results` field is a heterogeneous array (a
  `{tool_use_id, content: [{title,url}]}` block mixed with a plain-string
  commentary element), not a single freeform field `budgetBody` can act on
  the way it can for Grep's `content`. Search result counts are already
  small in practice, too.
- **Read** — has no confirmed `tool_response` schema at all; it's absent
  from the CLI's shipped `sdk-tools.d.ts` output types entirely, unlike
  Grep/Glob which are both there. Guessing a file-content field to truncate
  risks silently corrupting real file content, a worse failure mode than
  leaving it untouched.

All three exclusions are enforced by `handlePostToolUse`'s `default` case
(falls through to untouched pass-through) and covered by
`TestHandleWebSearchIsNeverBudgeted` / the existing Read-focused tests in
`main_test.go`.

**Follow-up: budget-trims now archive the full pre-trim content for
recovery.** Prompted by a direct question — could truncating Grep/Glob
output cause the agent to need *more* tool calls, not fewer, if it loses
something it needed? Both papers say yes, this is a real failure mode, but
also show why their own strategies mostly avoid it:

- AgentDiet's Step/PStep metrics (Table 2) measure exactly this — "an
  increase in steps indicates that some information is inappropriately
  reduced, disturbing the agent and requiring it to recover the information
  with additional steps." Their aggressive `Delete` baseline does cause
  >13% more steps. It avoids wrecking Pass% only because of two properties:
  reduction never touches a step until it's `a=2` steps old (never the
  freshest turn), and even then the model can recover by re-issuing a tool
  call.
- Complexity Trap's Observation Masking has the same shape: it only masks
  observations *older than* its rolling window `M` — the current and last
  `M-1` turns are always left intact.

Our budgeting fired on arrival — the freshest turn, before the agent had
even read the output once — which is exactly the case both papers
structurally avoid. And unlike `handleRetireInvestigate` (which archives
full content to disk with a recoverable path), a budget-trim just discarded
the dropped middle outright, so the only recovery was re-running the same
tool call.

Fixed: `budgetBody` now archives the full pre-trim body via `retire.Store`
on every trim (Bash included, for consistency) and names that path in the
notice line (`"... — full output at <path>"` / `"... — full list at
<path>"`), the same recoverability guarantee retirement already gives
investigate calls. Recovery now costs one `Read` of a local path instead of
re-running the tool. Covered by
`TestHandleBashBudgetArchivesFullOutputForRecovery` and the archive-path
assertions added to the Grep/Glob budget tests, plus
`TestHandleSessionStartInvalidatesBudgetArchive` (archives are wiped on
`SessionStart`/`PostCompact` same as everything else — no substitute
survives a restart or compaction).

## 2. Token-based threshold instead of line count — done

Shipped. `budgetLineThreshold = 60` is now `budgetTokenThreshold = 500`
([main.go:103](cmd/ledger-hook/main.go#L103)) — the same magnitude as
AgentDiet's `θ=500` — checked against `estimatedTokens(body)`, a `len(body)/4`
proxy (no tokenizer dependency, doesn't need to be exact, just monotonic and
hard to dodge by rewrapping output onto fewer/longer lines the way a raw
line count was). `budgetBody`'s head/tail split still counts in lines
(`budgetHeadLines`/`budgetTailLines` unchanged at 15/15 — that's a display
concern, not the trigger), so it gained a second guard: if the token
estimate trips but the body is too short in *lines* to carve a head and a
tail out of (a pathological single huge line), it's left untouched rather
than slicing out of range. Applies uniformly to all four callers from §1
(`budgetStdout`/`budgetTextField`/`budgetEntryList`, i.e. Bash/Grep/Glob —
WebFetch and WebSearch remain excluded per §1's own scoping).

Test fallout: the three `*ThresholdBoundary` tests and every "clearly long
output" test previously hit the boundary by counting fixed-width lines
(`linesOfStdout`/`linesOfEntries` plus `budgetLineThreshold`). Byte-width
per line varied per generator (`"line"` vs `"file123.go"`), so there was no
single line count that maps onto a token threshold across all of them.
Replaced with `linesOfApproxTokens`/`entriesOfApproxTokens`
(`cmd/ledger-hook/main_test.go`), which build single-character-per-line
bodies sized so `len(body)` lands on an exact multiple that makes
`estimatedTokens(body)` equal the requested token count exactly (verified
by hand: `4*tokens` bytes for the freeform-body shape, `4*tokens+1` for the
join-with-no-trailing-newline entry-list shape) — so boundary tests can
still assert under/at/over precisely instead of guessing through a line
count. `linesOfStdout` had no remaining callers after the swap and was
deleted rather than left unused.

## 3. Retire Bash post-boundary too — done

Shipped. Post-boundary, a long (over `budgetTokenThreshold`) first-time Bash
call is now archived via `retire.Store` and replaced with a compact receipt
(`handleBashRetire`, `cmd/ledger-hook/main.go`), the same recovery guarantee
`handleRetireInvestigate` already gives investigate-classified tools —
instead of just keeping a head/tail slice visible via `budgetStdout`. Short
first-time output, and exact repeats (still deduped via the ledger, cheaper
than archiving since the content already appeared once), are unaffected.

Went with "retirement triggered independent of the investigate/implement
tool split," not "reclassify Bash as investigate-like": `pastBoundary` is
threaded straight into `handleBashPostToolUse`, which picks retirement over
budgeting on its own. Reclassifying Bash into `investigateTools` was
rejected — that map also drives `handlePhaseBoundary`'s crossing detection,
and Bash is deliberately excluded there for a real reason (tool_name alone
can't tell a read-only command from a mutating one); folding it in for
retirement's sake would have reopened that.

Implementing this exposed a real, previously-latent bug: `handlePhaseBoundary`
returned a hardcoded `pastBoundary = false` for any tool outside
`investigateTools`/`implementTools`, without ever reading the actual phase
state — harmless before, since Bash never consumed that return value, but
wrong the moment it started to (caught by
`TestHandleRetiresLongBashOutputPostBoundary` failing against real budgeted
output instead of a retire receipt). Fixed by loading `phase.State` before
the classification branch and returning `st.Suggested` for unclassified
tools instead of a hardcoded `false`, while still leaving `st.Observe`
un-called for them (they still never advance or cross the boundary
themselves). Covered by `TestHandleRetiresLongBashOutputPostBoundary`,
`TestHandleBashDedupTakesPrecedenceOverRetirementPostBoundary`,
`TestHandleSessionStartInvalidatesBashRetireArchive`, and
`TestRetiredBytesOnBashRetiredCall`.

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

## Suggested order

1 → 2 (same PR, small) → 3 → decide on 4.

1, 2, and 3 are done. Next up: decide on 4.
