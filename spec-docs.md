# Going public: docs gap analysis + plan

Scoping doc for making this repo's documentation ready for a public
open-source launch. Written before touching README.md/CONTRIBUTING.md/etc.
so the plan is agreed on paper first. Builds on
[issue #20](https://github.com/umitkaanusta/agent-winglet/issues/20) (which
this doc supersedes in detail, not in intent) and is informed by reviewing
a comparable public repo in the same space — a desktop app that reduces
Claude Code/Codex token costs, paid product with an open-source client
shell — as a "what does this look like once it's public" reference.

## Borrow the substance, not the shape

That reference repo is used here as a gap-check, not a template. The goal
is a README that reads like it belongs to *this* project — same terse,
table-heavy, technical voice the current README already has (see its OS
install table, its exact `~/.agent-winglet/projects/...` path callouts) —
not a reskinned copy of someone else's. Concretely: their exact section
titles, their blockquote-callout styling, their section ordering, and their
overall prose-forward tone don't carry over. What does carry over is the
*category* of information missing below (does the README disclose pricing,
does it disclose what gets touched on disk, does it explain the mechanism)
— each gets written from scratch in this repo's own style.

## Gap analysis

What a comparable, credible public repo in this space has that points at a
real gap in *content* here, not a missing section to clone verbatim:

| Area | State here | Gap |
|---|---|---|
| License file | `LICENSE` (Apache 2.0), badge already links to it | None |
| Pricing disclosure | Nothing — README doesn't mention pricing at all | **Real gap.** See below. |
| Disclosure of what gets touched on disk (config edits, install locations, on uninstall) | Partial — Install/Uninstall sections describe *what* gets installed but aren't framed as a single trust-building disclosure | **Real gap**, directly relevant since this app edits `~/.claude/settings.json` and `${CODEX_HOME}/hooks.json` |
| Benchmarks/numbers section | Nothing in README — the numbers exist (`llms.txt`, `papers/README.md`) but aren't surfaced in-repo | **Real gap.** llms.txt has ready-to-use tables. |
| "How it works" section explaining the mechanism in plain terms | Nothing in README (mechanism is undocumented outside the app UI and `llms.txt`) | **Real gap.** |
| Product screenshots in README | None exist anywhere in the repo (only `branding/` logo + favicons) | **Real gap**, but not blocking — can ship without, add later |
| `docs/` folder (architecture, release process, smoke-test checklists) | `design.md` at root (frontend design decisions only) | Partial gap — no architecture/release-process doc, no smoke-test checklist docs |
| Project structure tree in README | None | Minor gap, cheap to add |
| Release/branching flow doc for contributors | None — release.yml exists but isn't documented outside the workflow file itself | Gap, but lower priority — we don't have a staging channel to document yet |
| `CONTRIBUTING.md` | None | **Not actually a gap** — the reference repo doesn't have one either despite being a live public repo with real external users. Drop this from scope; revisit only if contributors actually show up asking. |
| `SECURITY.md` | None | Same as above — not present in the reference repo either. Drop from scope for now. |
| `.github/ISSUE_TEMPLATE/` | None | Same — not present in the reference repo either. Not worth building speculatively. |

**Correction to issue #20**: that issue lists `CONTRIBUTING.md` and
`SECURITY.md` as tasks. Having now checked what a comparable live public repo
actually ships with, neither is load-bearing for a credible public launch.
Recommend closing those two checklist items as won't-do-for-now rather than
carrying them forward — cheap to add later if actual contributors ask, not
worth speculative maintenance today.

## The one gap that matters most: pricing disclosure

Winglet is a paid product ($2.50/mo or $30/yr, 3-day trial, per `llms.txt`)
with an entitlement-gated app (sign-in/subscribe nudges already implemented
per the app's own gating logic). Our README currently says nothing about
pricing anywhere. A public visitor cloning and building this repo will hit
the entitlement gate with zero warning that this is a paid app, not a free
OSS tool with a paid README badge for show.

Fix: a one- or two-line note folded into this README's existing intro
paragraph (which already states what the app does in plain prose) — no new
visual convention this README doesn't otherwise use, just pricing has to be
visible before someone reads the Install section.

## Content plan: `llms.txt` → README

`agentwinglet.com/llms.txt` already contains marketing-site copy that maps
cleanly onto README sections we're currently missing. Plan is to adapt
(not copy verbatim — llms.txt is written for the landing page, README is
written for a developer deciding whether to build/trust this repo):

| llms.txt section | → README section | Notes |
|---|---|---|
| Summary / headline | Fold into the existing intro line under the title | Keep it short — one line, not the full landing-page pitch |
| Pricing | Fold into the existing intro paragraph (see above) | Pull just the essentials: price, trial length, "same price across all plans" |
| How It Works (retire / trim / dedupe / smart compact table) | New "How it works" section, same table format the README already uses elsewhere | This table is good as-is, minimal editing needed |
| Performance Comparison (savings %, evaluated-on benchmarks) | New "Performance" section, same table format | Use the Winglet row only — skip the competitive comparison rows against other tools, that's marketing-site content, not appropriate for a neutral README |
| Research citations | Link to `papers/README.md` (already exists, already has the same two papers) instead of duplicating | Avoid drift between two copies of the same citation list |
| Important URLs | Footer links (website, contact) | Small addition, not a new section |

Not pulling from llms.txt: the savings calculator, the multi-plan
return-multiple table, and the competitor comparison rows — those belong on
the marketing site, not in a project README whose job is "should I
build/trust this," not "how does this compare commercially."

## Tightening the existing Install/Uninstall sections

The current README's Install and Uninstall sections already cover most of
what a security-conscious visitor needs (what gets written where, that
config merges are non-destructive, what each uninstall flag removes) — this
is a consolidation/tightening pass on content that already exists in this
repo's own established format, not a new section modeled on anything
external:

- Install: hook config merged into `~/.claude/settings.json` /
  `${CODEX_HOME}/hooks.json` (never overwrites existing entries), app
  installed to the per-OS standard location (already tabulated),
  `~/.agent-winglet/` created for the project registry and stats ledger.
- Uninstall: exact reverse, plus optional `--purge-binary`/`--purge-data`
  (already documented).
- One line worth adding since it isn't currently stated outright anywhere:
  nothing phones home — per `llms.txt`, all usage/savings data stays on the
  user's machine. This is a factual addition to existing prose, not a new
  section.

## Sequencing against other in-flight issues

- **#21 (install script GOPRIVATE removal)** and **#22 (org rename)** both
  touch README install instructions directly (the clone URL, the `go
  install` target, the private-repo error copy). This docs pass should land
  **after** both, or the README gets edited twice for the same lines.
  Recommended order: #22 (rename) → #21 (install script) → this docs pass,
  since the rename changes URLs everywhere and doing it last means rewriting
  fresh doc content around a moving target.
- This doc's scope is README-focused; it doesn't block or get blocked by
  #18 (self-update) — that just adds one more line once it ships ("how
  updates work"), not a structural change.

## Out of scope here

- Docs-site/landing-page copy changes (agentwinglet.com itself) — tracked
  separately in #20, not part of this repo's files.
- `CONTRIBUTING.md`, `SECURITY.md`, issue templates — deliberately dropped,
  see gap analysis above.
- Screenshots — worth adding eventually, but no product screenshots exist
  in this repo yet and capturing/cropping them is a separate, non-doc task.
  Flagging so it doesn't get silently forgotten, not committing to it in
  this pass.

## Next steps

1. Confirm this plan (this doc) before editing any files.
2. Once confirmed: rewrite `README.md` per the content plan above — pricing
   folded into the intro, new How-it-works and Performance sections, a
   tightened Install/Uninstall — after #22 and #21 land (see Sequencing).
   Written in this README's existing voice throughout.
3. Update issue #20's task list to match this doc's scope (drop
   CONTRIBUTING.md/SECURITY.md, add the pricing-disclosure and
   install/uninstall-tightening items explicitly).
