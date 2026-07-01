# Codog

Codog is a Go-native terminal coding agent for local repositories.

It combines model streaming, workspace tools, permission checks, and resumable
session state in a single inspectable binary. The goal is a Claude-Code-style
developer workflow that is easy to run locally, debug, and extend in Go.

## Status

Codog is under active development. The core agent loop, provider streaming,
workspace tools, permissions, sessions, REPL, TUI, slash commands, MCP, hooks,
skills, usage tracking, and plugin surfaces are present, but the project should
still be treated as experimental software.

For the exact feature surface of the build you are running:

```sh
codog capabilities
```

## Quick Start

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY=<key>
codog -p "read this repository and explain how it is organized"
```

For an interactive session:

```sh
codog repl
```

For the Bubble Tea interface:

```sh
codog tui
```

## Why Codog

Most coding agents are powerful, but the moving parts can be difficult to
inspect. Codog keeps those parts close to the repository:

- a single Go binary for normal use
- plain local files for configuration, memory, hooks, and sessions
- JSONL session records that can be resumed, inspected, exported, and compacted
- explicit permission checks before shell commands and workspace writes
- provider routing for Anthropic and OpenAI-compatible APIs
- extension points for slash commands, skills, hooks, MCP, and plugins

Codog is not meant to hide agent state behind a service. It is meant to make the
agent loop and its local side effects understandable.

## Core Workflow

A run usually follows this shape:

1. Codog loads repository instructions, configuration, memory, focused paths,
   and optional session history.
2. The selected model streams a response.
3. Tool calls are checked against the active permission mode.
4. Messages, approvals, tool calls, usage, summaries, and metadata are written
   to local session state.

Useful entry points:

| Task | Command |
| --- | --- |
| Ask once | `codog -p "summarize the test strategy"` |
| Continue a session | `codog --resume latest -p "implement the next fix"` |
| Work interactively | `codog repl` or `codog tui` |
| Inspect the environment | `codog doctor` |
| See current capabilities | `codog capabilities` |
| Restrict execution | `codog --permission-mode read-only -p "map the codebase"` |

## What It Can Do

Codog's current surface includes:

- one-shot prompts, side questions, REPL, and TUI sessions
- Anthropic Messages streaming and OpenAI-compatible streaming
- workspace tools for shell commands, file reads, writes, edits, grep, glob,
  patches, git operations, notebooks, and code-intelligence queries
- permission modes, allow and deny rules, approval records, and audit events
- JSONL sessions with resume, export, summaries, rewind, usage, cost, and
  compaction helpers
- slash commands, prompt templates, skills, hooks, MCP client/server pieces,
  plugin metadata, and marketplace-style commands
- background tasks, scheduled prompts, local agents, review helpers, updater
  commands, policy reports, and editor or remote-control bridge surfaces

Some advanced areas are compatibility-oriented rather than production hardened.
Validate sandbox, enterprise, updater, remote, and IDE workflows in your own
environment before relying on them.

## Providers

Anthropic is the default provider.

```sh
export ANTHROPIC_API_KEY=<key>
codog -p "review the current diff"
```

OpenAI-compatible providers are selected by model aliases and prefixes:

| Provider | Model selection | Credential |
| --- | --- | --- |
| OpenAI-compatible | `openai/...` | `OPENAI_API_KEY` |
| xAI | `grok`, `grok-mini`, or `xai/...` | `XAI_API_KEY` |
| DashScope | `qwen...` or `qwen/...` | `DASHSCOPE_API_KEY` |
| Custom Anthropic-compatible | configured base URL | `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` |

Use `codog providers status` to inspect the active provider and available
presets.

## Configuration

Configuration is layered so repository defaults and personal preferences can
stay separate.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Repository instructions loaded into the agent context |
| `.codog.json` | Shared project configuration |
| `.codog.local.json` | Uncommitted local overrides |
| `.codog/commands` | Project slash commands |
| `.codog/skills` | Project skills |
| `.codog/hooks` | Project hooks and local automation |

Secrets should live in environment variables or local-only config, not in files
committed to the repository.

## Safety Model

Codog separates a model suggestion from local execution. Tools declare the
permissions they need, and the runtime checks the active mode before running
commands or writing files.

Common modes:

| Mode | Use it for |
| --- | --- |
| `read-only` | Codebase inspection and planning |
| `workspace-write` | Normal edits inside the trusted workspace |
| `prompt` | Interactive confirmation before sensitive actions |
| `danger-full-access` or `allow` | Explicitly unrestricted local use |

These controls are development guardrails, not a hardened boundary for
untrusted code.

## When Codog Is Not the Right Tool

Codog is probably not the right choice today if you need:

- a vendor-supported production coding agent
- guaranteed behavioral parity with Claude Code
- hardened isolation for untrusted repositories or commands
- polished enterprise administration across a large organization

Those are long-term directions, not promises of the current build.

## Development

The CLI entry point is `cmd/codog`. Most behavior lives under `internal/`,
split by responsibility: agent loop, providers, tools, sessions, configuration,
hooks, skills, plugins, MCP, background work, code intelligence, and TUI.

Local checks:

```sh
go test ./...
go build ./cmd/codog
```

Keep generated state, secrets, local paths, and tool-attribution signatures out
of commits.

## License

Codog is released under the [MIT License](LICENSE).
