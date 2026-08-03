# agent-winglet
Get X% more usage on the same Claude Code / Codex plan

## Savings receipt

At the end of a session (if at least one mechanism fired), agent-winglet
prints a one-line summary of what it did — repeat commands deduped, long
outputs trimmed, investigate calls retired post-boundary — plus a running
lifetime total across sessions. It reports raw suppressed-content counts,
not a cost or token-savings figure (no such measurement exists yet). Set
`AGENT_WINGLET_QUIET=1` to suppress the message; every underlying mechanism
stays active either way.

## Install (v1: Session Ledger)

This repo is private, so there's no public curl-one-liner — clone it and run
`install.sh` from a checkout instead. It doesn't need to be run from inside
whichever project you want the hook active in; it installs the hook
globally by default, not per-project.

```
git clone https://github.com/umitkaanusta/agent-winglet.git
cd agent-winglet
./install.sh
```

This runs `go install` to fetch the `ledger-hook` binary and merges the hook
config into `~/.claude/settings.json` (Claude Code's user-level settings),
without touching any existing settings there. Claude Code merges user-level
hooks with any project-level ones, so this makes the hook active for every
project's Claude Code sessions from then on — no per-project install step.
The underlying mechanism was always project-scoped (ledger/stats state
lives under each project's own `.claude/agent-winglet/`, keyed off the
session's working directory), so installing once globally doesn't change
per-project isolation, just where the hook gets registered.

Because the repo is private, `go install` can't resolve it through the
public module proxy — `install.sh` sets `GOPRIVATE` itself so `go` fetches
straight from git instead, using whatever git credentials you already have
configured for github.com (an SSH key, or `gh auth setup-git` for HTTPS).
If that's not set up yet, `install.sh` will fail with a pointer to fix it.

The hook itself registers whichever project it's running in — the first
time it fires there — into `~/.agent-winglet/projects.json` (deduped,
pruned of stale/deleted entries on each run), so the desktop app's Projects
screen fills in as you use Claude Code, without install.sh needing to know
about every project up front. See "Desktop app" below for what reads that
file.

Pass `--local` to install into just the current directory's project instead
(`./.claude/settings.json`), the way this script worked before the hook
became global by default. **Migration note:** if a project already has a
per-project install from before, remove its `ledger-hook` entry from that
project's `.claude/settings.json` after installing globally — running both
at once fires the hook twice per event for that project and will
double-count stats and corrupt the ledger's turn tracking.

## Update

Re-run `install.sh` — it always installs `@latest` (the tip of `main`) and
the hook-config merge is a no-op if the entries are already there, so it's
safe to run repeatedly:

```
cd agent-winglet && git pull && ./install.sh
```

(`git pull` isn't actually required — `go install` fetches straight from
GitHub regardless of what your local checkout is at — but keeping the
checkout current avoids drift between what you read in the repo and what's
actually installed.)

## Uninstall

```
./uninstall.sh
```

Strips every `ledger-hook` entry out of `~/.claude/settings.json`, leaving
everything else in that file untouched. Same `--local` flag as `install.sh`
to target `./.claude/settings.json` instead. Add:

- `--purge-binary` to also delete the installed `ledger-hook` binary
- `--purge-data` to also delete `~/.agent-winglet` (the global project
  registry and quiet-mode config) and every registered project's
  `.claude/agent-winglet/` (that project's savings-ledger/stats history) —
  lists exactly what it's about to delete and asks for confirmation first,
  unless `-y`/`--yes` is also passed

`./uninstall.sh --purge-binary --purge-data -y` does a full teardown.

## Desktop app

`cmd/agent-winglet-app` is a small cross-platform dashboard (Wails — Go
backend, OS-native webview frontend) that shows the savings receipt data
above without reading JSON files by hand: an Overview screen with a
lifetime hero stat and per-mechanism cards summed across every registered
project, a Projects screen breaking that down per project (and showing
whether the hook is actually wired into that project's `.claude/settings.json`
right now, not just registry presence), and a Settings screen for the one
wired toggle today — quiet mode, via `~/.agent-winglet/config.json`
(`AGENT_WINGLET_QUIET`, if set, still takes precedence for that session).

No system-tray/menu-bar icon: building `getlantern/systray` alongside Wails
in the same binary fails at link time on macOS (both declare an Objective-C
class named `AppDelegate`, which collides as a duplicate symbol) — not a
design choice. Dock/taskbar icon only.

```
make app   # builds cmd/agent-winglet-app into a native .app/.exe/binary
```

On macOS this requires `CGO_LDFLAGS="-framework UniformTypeIdentifiers"`
(see the Makefile comment) — recent Xcode SDKs don't pull that framework
in as a transitive link the way Wails' darwin frontend code expects.
`make app` sets this for you.

## Developing this repo

```
make build   # builds bin/ledger-hook locally
make test    # runs the Go test suite
```

This repo's own `.claude/settings.json` is a dev/test fixture — it points
at the locally-built `bin/ledger-hook` so the hook can be exercised against
this repo itself while working on it. It is not the install mechanism end
users go through; that's `install.sh`/`uninstall.sh` above.
