<h1 align="center">Winglet</h1>

<p align="center">
  Make your Claude Code / Codex plan go 50% further.
</p>

<p align="center">
  <a href="https://agentwinglet.com">Install</a> | 
  <a href="https://agentwinglet.com/#performance-comparison">Performance</a> |
  <a href="https://agentwinglet.com/#how-it-works">Methodology</a> |
  <a href="https://agentwinglet.com/#pricing">Pricing</a>
</p>

<p align="center">
  Winglet cuts stale reads, repeated tool output, and wasted context before 
  they reach the model. <br/>
  Everything stays on your machine.
</p>

<p align="center">
  <img src="branding/winglet-demo.gif" alt="Winglet demo" width="900">
</p>

---

Winglet is a paid desktop app: $2.50/mo, billed yearly at $30. One plan covers
every Claude Code and Codex tier. This repo contains the hooks that connect
Winglet to your coding agent, plus the desktop app for tracking savings.

Winglet stands on the shoulders of two giants: [AgentDiet: Reducing Cost of LLM
Agents with Trajectory Reduction](https://doi.org/10.1145/3797084) and [The
Complexity Trap: Simple Observation Masking Is as Efficient as LLM
Summarization for Agent Context Management](https://arxiv.org/abs/2508.21433).

## Performance
Measured on industry-standard agentic coding benchmarks 
[SWE-bench Verified](https://www.swebench.com/verified.html) and
[Multi-SWE-bench Flash](https://huggingface.co/datasets/ByteDance-Seed/Multi-SWE-bench-flash).

**Results:**
- Usage gained from cost savings: **26.7%-56.0%**
- Computational cost savings: **21.1%–35.9%**
- Input token savings: **21.1-35.9%**
- Answer quality: **Stays the same**, ranging between -1.0% and +2.0%.

Note: Results may vary by workload and workflow.

## How it works

The main idea is **trajectory reduction:** send less junk to the model.

Winglet is a desktop app. It integrates with Claude Code and Codex through
hooks, so it can optimize what gets sent to the model while your agent thinks
and uses tools. It runs locally, and no data leaves your machine.

### Retire used research

Your agent searches the codebase to find a function. Once it finds it, the full
search trail is no longer needed while writing code.

Winglet sends the model a short note and keeps the full output on disk if
needed.

### Trim long tool output

80% of useful information is usually in the first and last 15 lines. Winglet
sends that head and tail, and keeps the full output on disk if needed.

This also implicitly steers the model toward focused searches.

### Cut duplicates in context

Sometimes the agent asks for the same thing twice. If the output is unchanged,
it is already in context.

Winglet does not send it to the model again.

### Smart compact

Winglet offers to compact after the agent uses what it found. It keeps the
current working context intact while reducing older waste, which saves more
usage.

Note: Compacting is optional. It is not part of Winglet's "usage saved"
calculation.

## Requirements

To use Winglet, you need:

- Claude Code, Codex, or both.
- The Winglet desktop app.
- A Winglet subscription, or the 3-day free trial. No credit card required.

To install from source, you also need Go, Node.js/npm, and `jq`.

## Supported platforms

Winglet supports macOS and Linux. Windows support is coming soon.

## Installation

### Install from Winglet

Download Winglet from [agentwinglet.com](https://agentwinglet.com), open the
desktop app, then follow Settings > Installation to connect Claude Code, Codex,
or both.

### Install from source

You can also clone the repo and run the installer:

```sh
git clone https://github.com/AgentWinglet/agent-winglet.git
cd agent-winglet
./install.sh
```

By default, this installs the desktop app and configures hooks for both Claude
Code and Codex.

Useful options:

```sh
./install.sh --hook-only
./install.sh --app-only
./install.sh --claude-only
./install.sh --codex-only
./install.sh --local
```

To uninstall:

```sh
./uninstall.sh
```

## Privacy

Winglet runs locally. Your codebase, prompts, hook output, saved tool logs, and
savings data stay on your machine.

Winglet servers only receive what is needed for accounts, trials, subscriptions,
and app licensing: name, email address, sign-in provider, device/app metadata,
and subscription or trial status.

## Development

Developer setup is mostly for building from source or working on the agent
integrations.

- Go 1.26.5
- Node.js/npm
- Wails 2.13.0
- `jq`

Common commands:

```sh
make build
make test
make app
```

On Linux, app builds may also need GTK/WebKit development packages. On Ubuntu:

```sh
sudo apt-get install -y libgtk-3-dev libwebkit2gtk-4.1-dev
```

## Support

For help with installation or billing, email [hi@agentwinglet.com](mailto:hi@agentwinglet.com).

## License
Apache 2.0 — see [LICENSE](LICENSE).
