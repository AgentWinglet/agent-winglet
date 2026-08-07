# Winglet

**Get more Claude Code usage out of the same weekly cap.**

[![CI](https://github.com/umitkaanusta/agent-winglet/actions/workflows/ci.yml/badge.svg)](https://github.com/umitkaanusta/agent-winglet/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

## Install

This repo is private, so there's no public curl-one-liner — clone it and run
`install.sh` from a checkout instead:

```
git clone https://github.com/umitkaanusta/agent-winglet.git
cd agent-winglet
./install.sh
```

By default this installs **both** the Session Ledger hook and the desktop
app (`Winglet`). Pass `--hook-only` or `--app-only` to install just one.

**The hook** (`claude-hook`): fetched with `go install`, so it doesn't
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
concept.

**The app**: unlike the hook, this is always built from your local
checkout — its frontend build output isn't checked into git (it's
generated), so `go install` can't fetch a working copy of it the way it can
for the hook. `install.sh` builds it (`make app` under the hood) and installs
the result to the standard per-OS location:

| OS      | Installed to                                          |
|---------|--------------------------------------------------------|
| macOS   | `/Applications/Winglet.app`                       |
| Linux   | `~/.local/bin/Winglet` + a `~/.local/share/applications` launcher entry |
| Windows | `%LOCALAPPDATA%\Programs\Winglet\` + a Start Menu shortcut (via Git Bash/MSYS2 — needs `powershell.exe` on PATH for the shortcut, which is skipped, not fatal, if it's missing) |

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

- `--purge-binary` to also delete the installed `claude-hook` binary
- `--purge-data` to also delete `~/.agent-winglet` (the global project
  registry and quiet-mode config) and every registered project's
  `.claude/agent-winglet/` (that project's savings-ledger/stats history) —
  lists exactly what it's about to delete and asks for confirmation first,
  unless `-y`/`--yes` is also passed

`./uninstall.sh --purge-binary --purge-data -y` does a full teardown.

## Developing this repo

```
make build   # builds bin/claude-hook locally
make test    # runs the Go test suite
```

This repo's own `.claude/settings.json` is a dev/test fixture — it points
at the locally-built `bin/claude-hook` so the hook can be exercised against
this repo itself while working on it. It is not the install mechanism end
users go through; that's `install.sh`/`uninstall.sh` above.

## License

MIT — see [LICENSE](LICENSE).
