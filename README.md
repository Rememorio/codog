# Codog

Codog is an experimental, Go-native terminal coding agent inspired by Claude
Code. It is built as a single local binary with streaming model responses,
permission-gated workspace tools, JSONL sessions, and an agent runtime whose
moving parts are visible in the repository.

The goal is not to present a black-box assistant. Codog is for developers who
want to inspect, extend, and reason about the loop that reads code, asks a
model, checks permissions, runs tools, and records what happened.

Codog is not an official Anthropic product. It is also not a hardened sandbox
for hostile repositories or untrusted commands.

## Why Codog

- **Local-first agent state**: sessions, summaries, approvals, tool calls, and
  usage records are stored locally instead of hidden behind a hosted UI.
- **Single Go binary**: the runtime is easy to build, ship, debug, and inspect
  without a large language-specific wrapper stack.
- **Permission-aware tools**: model requests pass through explicit permission
  checks before shell commands, edits, or broader system access run.
- **Extensible by design**: slash commands, skills, hooks, MCP, plugins, and
  provider routing are first-class parts of the runtime.
- **Compatibility-oriented**: the project is growing toward the workflows people
  expect from Claude Code while keeping implementation details open.

## Quick Start

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY=<key>
codog -p "summarize this repository"
```

For interactive work:

```sh
codog repl
codog tui
```

To continue a previous session:

```sh
codog --resume latest -p "continue from the last task"
```

Run `codog help` for the full command reference. The README intentionally stays
focused on orientation instead of duplicating every subcommand.

## What It Can Do

| Area | Current surface |
| --- | --- |
| Agent loop | one-shot prompts, REPL, Bubble Tea TUI, streaming responses, session resume |
| Providers | Anthropic by default, OpenAI-compatible routing, provider status checks |
| Workspace tools | bash, read, write, edit, grep, glob, patch, git, notebooks, code intelligence |
| Safety controls | permission modes, allow/deny rules, audit records, trust checks |
| Session state | JSONL transcripts, summaries, rewind, export, cost and token usage, compaction |
| Customization | slash commands, prompt templates, skills, hooks, memory, output styles |
| Integrations | MCP client/server surfaces, IDE bridge, remote control, plugins, background work |
| Validation | doctor checks, mock Anthropic server, parity harnesses, contract reports |

The advanced areas are still evolving. Treat Codog as an active implementation
project, not as a guaranteed drop-in replacement for Claude Code.

## How It Works

At a high level, each run follows the same shape:

```text
project instructions + config + optional session history
        |
        v
model request and streaming response
        |
        v
tool proposal from the model
        |
        v
permission and trust checks
        |
        v
local execution and JSONL session recording
```

That loop is deliberately kept in ordinary Go code. The core CLI lives in
`cmd/codog`, while the runtime modules under `internal/` cover the agent loop,
tools, sessions, providers, configuration, permissions, hooks, skills, MCP,
TUI, background tasks, and compatibility harnesses.

## Configuration

Configuration is layered so shared project behavior and personal preferences can
stay separate.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Repository instructions loaded into the agent context |
| `.codog.json` | Shared project configuration |
| `.codog.local.json` | Uncommitted local overrides |
| `.codog/commands` | Project slash commands |
| `.codog/skills` | Project skills |
| `.codog/hooks` | Project automation hooks |

Keep secrets in environment variables or local-only config. Do not commit API
keys, local machine paths, or generated session state.

## Providers

Anthropic is the default provider. Other providers can be selected through model
names, aliases, and configuration.

| Provider | Typical selection | Credential |
| --- | --- | --- |
| Anthropic | default Claude model | `ANTHROPIC_API_KEY` |
| OpenAI-compatible | `openai/...` | `OPENAI_API_KEY` |
| xAI | `grok`, `grok-mini`, or `xai/...` | `XAI_API_KEY` |
| DashScope | `qwen...` or `qwen/...` | `DASHSCOPE_API_KEY` |
| Custom Anthropic-compatible | configured base URL | `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` |

Use `codog providers status` to inspect the active provider setup.

## Safety Model

Codog separates model intent from local execution. Tools declare the access they
need, and the runtime checks each request against the active mode before it runs.

| Mode | Intended use |
| --- | --- |
| `read-only` | Inspect code and plan changes |
| `workspace-write` | Edit files inside the current workspace |
| `prompt` | Ask before sensitive actions |
| `danger-full-access` or `allow` | Explicitly unrestricted local use |

These controls are development guardrails, not a security boundary. Review what
the model asks to run, especially in unfamiliar repositories.

## Development

Build and test from the repository root:

```sh
go test ./...
go build ./cmd/codog
```

Useful entry points:

| Path | Description |
| --- | --- |
| `cmd/codog` | CLI entry point |
| `internal/agent` | command dispatch and agent orchestration |
| `internal/tools` | local tool implementations |
| `internal/session` | JSONL session storage and resume support |
| `internal/config` | layered configuration loading and mutation |
| `internal/tui` | Bubble Tea interface |
| `internal/mcp` | MCP integration |
| `internal/skills` | skill discovery and rendering |

Keep generated state, secrets, local paths, and tool-attribution signatures out
of commits.

## Project Status

Codog is under active development. The core surfaces exist, but compatibility
work, cross-platform hardening, and advanced workflows still need careful
validation before serious use.

Codog is probably not the right tool today if you need vendor support, guaranteed
Claude Code parity, enterprise administration, or hardened isolation.

## License

Codog is released under the [MIT License](LICENSE).
