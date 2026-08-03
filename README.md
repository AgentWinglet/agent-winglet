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

## Install

This repo is private, so there's no public curl-one-liner — clone it and run
`install.sh` from a checkout instead:

```
git clone https://github.com/umitkaanusta/agent-winglet.git
cd agent-winglet
./install.sh
```

By default this installs **both** the Session Ledger hook and the desktop
app (`AgentWinglet`). Pass `--hook-only` or `--app-only` to install just one.

**The hook** (`ledger-hook`): fetched with `go install`, so it doesn't
matter where you run this from, and it works even if you invoke `--hook-only`
from outside a checkout. It merges its config into `~/.claude/settings.json`
(Claude Code's user-level settings) without touching any existing settings
there. Claude Code merges user-level hooks with any project-level ones, so
this makes the hook active for every project's Claude Code sessions from
then on — no per-project install step. Ledger/stats state still lives under
each project's own `.claude/agent-winglet/` (keyed off the session's working
directory) — installing once globally doesn't change per-project isolation,
just where the hook gets registered. It also registers whichever project
it's running in — the first time it fires there — into
`~/.agent-winglet/projects.json` (deduped, pruned of stale/deleted entries
on each run), which is what feeds the desktop app's Projects screen.

Because the repo is private, `go install` can't resolve it through the
public module proxy — `install.sh` sets `GOPRIVATE` itself so `go` fetches
straight from git instead, using whatever git credentials you already have
configured for github.com (an SSH key, or `gh auth setup-git` for HTTPS).
If that's not set up yet, `install.sh` will fail with a pointer to fix it.

Pass `--local` to install the hook into just the current directory's project
instead (`./.claude/settings.json`) — hook scope only, the app has no such
concept. **Migration note:** if a project already has a per-project install
from before, remove its `ledger-hook` entry from that project's
`.claude/settings.json` after installing globally — running both at once
fires the hook twice per event for that project and will double-count stats
and corrupt the ledger's turn tracking.

**The app**: unlike the hook, this is always built from your local
checkout — its frontend build output isn't checked into git (it's
generated), so `go install` can't fetch a working copy of it the way it can
for the hook. `install.sh` builds it (`make app` under the hood — see
"Desktop app" below) and installs the result to the standard per-OS
location:

| OS      | Installed to                                          |
|---------|--------------------------------------------------------|
| macOS   | `/Applications/AgentWinglet.app`                       |
| Linux   | `~/.local/bin/AgentWinglet` + a `~/.local/share/applications` launcher entry |
| Windows | `%LOCALAPPDATA%\Programs\AgentWinglet\` + a Start Menu shortcut (via Git Bash/MSYS2 — needs `powershell.exe` on PATH for the shortcut, which is skipped, not fatal, if it's missing) |

Windows/Linux support is implemented to spec but has only been verified by
the CI smoke-test job (see `.github/workflows/app-build.yml`), not by hand
on those OSes — macOS is the one this has actually been run on end-to-end.

## Update

Re-run `install.sh` from inside your checkout:

```
cd agent-winglet && git pull && ./install.sh
```

Both the hook-config merge and the app install are idempotent, so this is
safe to run repeatedly. `git pull` is required for the app (it's always
built from local source) but not for the hook (`go install ...@latest`
always fetches fresh from GitHub regardless of your checkout's state) —
just always pull first, it's harmless either way.

## Uninstall

```
./uninstall.sh
```

By default removes **both** the hook wiring and the installed app. Same
`--hook-only`/`--app-only`/`--local` flags as `install.sh`. Uninstalling the
app doesn't need to be run from inside a checkout — it just removes files
from the known per-OS install locations above, it doesn't build anything.
Add:

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
make app   # builds cmd/agent-winglet-app into a native .app/.exe/binary,
           # at cmd/agent-winglet-app/build/bin/ — install.sh handles
           # copying that into the right place for your OS (see "Install")
```

On macOS this requires `CGO_LDFLAGS="-framework UniformTypeIdentifiers"`
(see the Makefile comment) — recent Xcode SDKs don't pull that framework
in as a transitive link the way Wails' darwin frontend code expects.
`make app` sets this for you. On Linux it probes `pkg-config` to pick the
right webkit2gtk build tag (Ubuntu 24.04+ needs `-tags webkit2_41`; older
versions don't) — same distinction the CI matrix in
`.github/workflows/app-build.yml` handles explicitly.

## Developing this repo

```
make build   # builds bin/ledger-hook locally
make test    # runs the Go test suite
```

This repo's own `.claude/settings.json` is a dev/test fixture — it points
at the locally-built `bin/ledger-hook` so the hook can be exercised against
this repo itself while working on it. It is not the install mechanism end
users go through; that's `install.sh`/`uninstall.sh` above.
