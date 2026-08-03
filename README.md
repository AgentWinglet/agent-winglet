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

## Install into your own project (v1: Session Ledger)

From the root of the project you want the hook active in (not from this
repo):

```
curl -fsSL https://raw.githubusercontent.com/umitkaanusta/agent-winglet/main/install.sh | bash
```

This runs `go install` to fetch the `ledger-hook` binary and merges the
hook config into that project's `.claude/settings.json`, without touching
any existing settings there. It also registers the project's absolute path
in `~/.agent-winglet/projects.json` (deduped, pruned of stale/deleted
entries on each run).

## Developing this repo

```
make build   # builds bin/ledger-hook locally
make test    # runs the Go test suite
```

This repo's own `.claude/settings.json` is a dev/test fixture — it points
at the locally-built `bin/ledger-hook` so the hook can be exercised against
this repo itself while working on it. It is not the install mechanism end
users go through; that's `install.sh` above.

`agent-winglet-v1-remaining.md` in this repo tracks what's built vs.
outstanding, including the deferred usage_per_solve measurement gate.
