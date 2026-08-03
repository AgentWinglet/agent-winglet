# SPEC: Move per-project state out of the target repo

## Problem

`ledger-hook` currently writes all per-project, per-session state under:

```
<project>/.claude/agent-winglet/
```

That path is only ignored by *this* repo's own [.gitignore](.gitignore) (`.claude/agent-winglet/`), because we dogfood the hook on ourselves. The hook installs **globally** (`~/.claude/settings.json`, see [install.sh](install.sh)) and fires in every project the user opens in Claude Code. For any other project — call it Person A's repo — nothing adds that ignore rule, so:

- `git status` in Person A's repo shows `.claude/agent-winglet/` as untracked clutter, permanently, in every session.
- It's one accidental `git add -A` away from being committed into a repo that has nothing to do with agent-winglet.
- Person A would have to manually patch their own `.gitignore` to make agent-winglet's presence invisible — an install-time step nobody will remember, for a tool that's supposed to be zero-touch.

There's a second symptom of the same root cause, visible in this very repo: the hook keys everything on `in.Cwd` — the raw `cwd` Claude Code passes per session — not on the project's identity. A session opened with its cwd at the repo root, another at `cmd/agent-winglet-app/`, and another at `cmd/agent-winglet-app/frontend/src/` produce **three independent** `.claude/agent-winglet/` directories for what is logically one project:

```
agent-winglet/.claude/agent-winglet/
agent-winglet/cmd/agent-winglet-app/.claude/agent-winglet/
agent-winglet/cmd/agent-winglet-app/frontend/src/.claude/agent-winglet/
```

So the fix needs to do two things, not one: get the data out of the tracked tree, *and* stop fragmenting one project into N state dirs based on whatever subdirectory a given session happened to start in.

## Current data inventory

Read directly from the code (`internal/{ledger,phase,retire,stats,registry,config}`):

| Data | Path today | Lifecycle | Written by |
|---|---|---|---|
| Session ledger (repeat-detection hashes) | `<project>/.claude/agent-winglet/<sessionID>.json` | Session-only — deleted on `SessionStart`/`PostCompact` | [ledger.go](internal/ledger/ledger.go) |
| Phase boundary state | `<project>/.claude/agent-winglet/<sessionID>.phase.json` | Session-only | [phase.go](internal/phase/phase.go) |
| Retired investigate content | `<project>/.claude/agent-winglet/<sessionID>.retired/*.txt` | Session-only | [retire.go](internal/retire/retire.go) |
| Session stats tally | `<project>/.claude/agent-winglet/<sessionID>.stats.json` | Session-only | [stats.go](internal/stats/stats.go) |
| Lifetime stats tally | `<project>/.claude/agent-winglet/lifetime.stats.json` | **Persists forever**, per project | [stats.go](internal/stats/stats.go) |

Already outside the project (no problem today, unchanged by this spec):

| Data | Path | Purpose |
|---|---|---|
| Global config (quiet flag) | `~/.agent-winglet/config.json` | [config.go](internal/config/config.go) |
| Project registry | `~/.agent-winglet/projects.json` | [registry.go](internal/registry/registry.go) — flat list of every project dir the hook has ever seen, used by the desktop app to enumerate projects and sum lifetime stats |

So the registry package already proves the right model: **global, per-user state, keyed by project path** — it's just that `ledger`/`phase`/`retire`/`stats` didn't follow it for their own per-project files.

## Strategy

Move all five per-project files/dirs from inside the target repo to a namespaced subdirectory of the existing global state root, keyed by a stable hash of the project's absolute path — the same pattern VS Code (`workspaceStorage/<hash>/`), direnv, and pnpm's store use to keep per-project caches out of the project tree.

```
~/.agent-winglet/
  config.json                 # unchanged
  projects.json                # unchanged
  projects/
    <basename>-<hash12>/       # one dir per project, e.g. agent-winglet-3f9a1c8b2e04
      <sessionID>.json
      <sessionID>.phase.json
      <sessionID>.retired/*.txt
      <sessionID>.stats.json
      lifetime.stats.json
```

- **Key derivation**: `hash12 = hex(sha256(filepath.Clean(abs(projectRoot))))[:12]`, where `projectRoot` is **not** the raw `cwd` — see below. Resolve to an absolute path first (`filepath.Abs`); do not attempt `EvalSymlinks` — the hook always receives a real `cwd` from Claude Code, and adding symlink resolution risks a project's key silently changing if a parent directory's symlink target moves, which is a worse failure mode than the rare symlink-alias collision it would prevent.
- **Project identity = git root, not cwd.** `projectRoot` is found by walking upward from `cwd` looking for a `.git` entry (directory or file — a file means a worktree, see below), stopping at the first one found. If none is found before the walk reaches the filesystem root (or `$HOME`, whichever comes first — see the `internal/projectroot` note below), `projectRoot` falls back to `cwd` itself, so non-git directories still get a stable, single state dir. This is what actually fixes the fragmentation above: three sessions opened at the repo root, `cmd/agent-winglet-app/`, and `cmd/agent-winglet-app/frontend/src/` all resolve to the same `projectRoot` (the repo root) and land in the same global state dir.
- **Worktrees**: a `git worktree`'s `.git` is a *file*, not a directory, pointing at the main repo's `.git/worktrees/<name>`. This design deliberately does *not* resolve that back to a shared root — each worktree's own directory (where its `.git` file lives) is treated as its own `projectRoot`. Session-scoped data (ledger/phase/retire/session-stats) doesn't care either way since it's wiped every session; lifetime stats simply accrue per-worktree, which is an acceptable simplification rather than a real requirement.
- **`<basename>-` prefix**: purely for human debuggability (`ls ~/.agent-winglet/projects/` is legible). Sanitize to `[A-Za-z0-9_-]`, collapsing anything else to `-`, capped at ~40 chars. The hash suffix is what actually guarantees uniqueness — two projects named `api` in different places still get distinct dirs.
- **Collision risk**: 12 hex chars = 48 bits. For a single user's set of local project directories this is not a real risk (same order of magnitude as a git abbreviated SHA); not worth the complexity of collision detection.

## Implementation

### 1. New package: `internal/projectroot`

```go
package projectroot

// Resolve walks upward from cwd looking for a .git entry (dir or file) and
// returns the directory that contains it. If none is found (cwd isn't
// inside a git repo, or the walk exhausts its bound), it returns cwd
// unchanged — every caller gets a usable project identity either way.
func Resolve(cwd string) string
```

This is the piece that fixes the fragmentation issue, independent of where the state ends up living. Implementation notes:

- Walk via `filepath.Dir` until `os.Stat(filepath.Join(dir, ".git"))` succeeds, `dir` stops changing (reached `/`), or a hop cap (e.g. 64) is hit as a defensive bound.
- Stop early if the walk would cross `$HOME`'s parent — i.e. never search above the user's home directory — so a stray `.git` somewhere unexpected up the tree can't make unrelated projects collapse into one identity.
- No network/subprocess calls (no shelling out to `git rev-parse --show-toplevel`) — the hook runs on every tool call, so this needs to be a handful of `os.Stat`s, not a process spawn.

### 2. New package: `internal/statedir`

```go
package statedir

// Dir returns the global per-project state directory for projectRoot,
// creating nothing. Callers MkdirAll as needed, same as they do today.
// projectRoot is expected to already be resolved (see internal/projectroot)
// — statedir itself is not git-aware, it just maps a path to a hashed
// subdirectory.
func Dir(projectRoot string) (string, error)
```

Centralizes the `~/.agent-winglet/projects/<basename>-<hash12>` computation in one place, so `ledger`, `phase`, `retire`, and `stats` all resolve to the same directory for the same project without duplicating the hash/sanitize logic four times. Deliberately kept dumb (pure path hashing, no git awareness) so the two concerns — "what is this project's identity" (`projectroot`) and "where does its state physically live" (`statedir`) — stay separable and independently testable.

### 3. `cmd/ledger-hook`: resolve once, at the top

In `handle` ([main.go:156](cmd/ledger-hook/main.go)), before any of the existing branches run:

```go
root := projectroot.Resolve(in.Cwd)
```

Then use `root` everywhere the code currently passes `in.Cwd` into `registry.Register`, `ledger.*`, `phase.*`, `retire.*`, and `stats.*`. This is the only change needed in this file beyond the migration step below — every downstream call site already just threads a project-identity string through, so swapping which string that is (`root` instead of raw `in.Cwd`) is a one-line change per call site, not a signature change.

One consequence worth calling out: `registry.json` itself will now dedupe on `root` too, so the three cwds from the example above collapse into a single registry entry — which also means `cmd/agent-winglet-app/app.go`'s lifetime-stats summary (currently one row per registered dir) stops double/triple-counting a single project that was worked on from multiple subdirectories.

### 4. `ledger`, `phase`, `retire`, `stats`: swap the path helper only

Each package's private path-building helper (`statePath`, `dir`, `sessionPath`, `lifetimePath`) changes from:

```go
filepath.Join(projectDir, ".claude", "agent-winglet", sessionID+".json")
```

to:

```go
d, err := statedir.Dir(projectDir)
...
filepath.Join(d, sessionID+".json")
```

**No exported function signature changes.** `Load(projectDir, sessionID)`, `Save(projectDir, sessionID, s)`, `Invalidate(projectDir, sessionID)`, `Store(projectDir, sessionID, content)`, `LoadSession`/`SaveSession`/`LoadLifetime`/`SaveLifetime` all keep taking a project-identity string as today — it's still the right key, it just now maps to a different physical location, and (per §3) the caller now passes the resolved git root instead of raw `cwd`. So the only caller-side change is the one already described in §3 (`cmd/ledger-hook/main.go` passing `root` instead of `in.Cwd`); `cmd/agent-winglet-app/app.go` needs no change since it already just iterates whatever `registry.Load()` returns.

The four path-helper functions can now return an error (since `statedir.Dir` calls `os.UserHomeDir()`, which can fail); propagate it the same way `Load`/`Save` already propagate other errors.

### 5. Migration for existing installs

Session-scoped data (ledger/phase/retire/session-stats) is designed to never survive a restart anyway — losing it mid-upgrade is a non-event. The only thing with lasting value is `lifetime.stats.json`, and it's a display-only counter, so even a lossy migration is low-stakes. Given that, do a best-effort, self-healing migration rather than a one-time script — but it has to **merge**, not just copy-if-absent, because a single project can have more than one stale old-style dir to reclaim (the repo-root/`cmd/agent-winglet-app/`/`cmd/agent-winglet-app/frontend/src/` example from the Problem section is exactly this case: three old dirs, one new destination).

In `registry.Register`'s caller (`handle` in [main.go](cmd/ledger-hook/main.go)), right after computing `root := projectroot.Resolve(in.Cwd)` and before/alongside `registry.Register(root)`:

1. Check for `<in.Cwd>/.claude/agent-winglet/lifetime.stats.json` — note this checks the **raw cwd**, not `root`, since that's where this particular session's stale data (if any) actually lives.
2. If present: load it, load the new location's lifetime tally (`stats.LoadLifetime(root)` — zero value if none yet), **add** the old tally into it field-by-field (same shape as `Lifetime.Add`, minus the `Sessions` bump since these aren't new sessions), and save back to `root`'s new location.
3. Either way, if `<in.Cwd>/.claude/agent-winglet/` exists, `os.RemoveAll` it.

Because this runs against `in.Cwd` (not `root`) on every `SessionStart`/`PostCompact`, each of the three stale per-subdirectory dirs gets folded in and cleaned up independently, the next time a session happens to start from that particular subdirectory — and since step 2 adds rather than overwrites, the order they get cleaned up in doesn't matter and nothing is lost if it happens across several separate sessions over time.

All best-effort — swallow errors (log to stderr, don't fail the hook), matching this codebase's existing fail-soft convention for state I/O (`ledger.Load`, `stats.loadJSON`, `registry.Load` all fail soft on corrupt/missing data).

### 6. `uninstall.sh --purge-data`

Currently loops over every registered project and deletes `<dir>/.claude/agent-winglet` individually ([uninstall.sh:184-190](uninstall.sh)). Once all per-project state lives under `~/.agent-winglet/projects/`, deleting `~/.agent-winglet` (which the script already does for the registry/config) covers it in one shot — delete the per-project loop entirely. Keep it only as a fallback for pre-migration leftovers (a project whose hook hasn't fired from every one of its old cwds since upgrading) — i.e. keep the loop, but it becomes a rare-case safety net rather than the primary mechanism.

### 7. `.gitignore`

Once the migration ships and this repo's own `.claude/agent-winglet/` stops being written to, drop the `.claude/agent-winglet/` line from [.gitignore](.gitignore). Safe to leave for one release as a transition safety net if preferred.

## Test impact

`ledger_test.go`, `phase_test.go`, `retire_test.go`, `stats_test.go` currently pass `dir := t.TempDir()` as `projectDir` and assert on `State`/round-trip behavior without touching physical paths directly — they don't depend on `HOME` today because `projectDir` *is* the write location. After this change, the write location depends on `os.UserHomeDir()`, so every such test needs `t.Setenv("HOME", t.TempDir())` added (same pattern already used in [registry_test.go](internal/registry/registry_test.go) and [config_test.go](internal/config/config_test.go)). No test assertions themselves need to change — they already only check `Load`/`Save`/`Invalidate` behavior through the package API, not raw paths.

Add new `statedir` tests: same project path → same dir across calls; different paths → different dirs; basename sanitization for paths with spaces/unicode/special chars.

Add new `projectroot` tests: cwd at a repo root, cwd several levels under a repo root, cwd with no `.git` anywhere above it (falls back to cwd), cwd inside a worktree (`.git` file, not dir) resolves to the worktree's own root not the main repo's.

## Out of scope

- Changing the *shape* of any stored file (JSON schemas unchanged).
- Changing `~/.agent-winglet/config.json` or `~/.agent-winglet/projects.json` locations — already correctly global.
- A user-facing migration command or dashboard notice — the self-healing migration in `registry.Register` is silent by design, consistent with how registration itself is already silent.
