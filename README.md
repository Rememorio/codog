# Codog

Codog is a Go-native coding agent CLI inspired by Claude Code. It is built as a
single local binary, streams model responses, runs workspace tools behind
permission checks, and stores sessions as JSONL so work can be resumed, reviewed,
or exported.

Codog is not an official Anthropic product. It is an experimental, hackable
implementation for people who want to study, extend, and run a local coding
agent with a small operational footprint.

## Why Codog?

Large coding agents are often hard to inspect because the runtime, UI, session
state, tool execution, and provider adapters are split across several services.
Codog keeps those pieces in one Go program:

- **Portable runtime:** build or install one binary and run it from any
  repository.
- **Transparent state:** sessions, summaries, todos, usage, and exports are
  plain local files.
- **Local-first control:** tool calls are checked by permission mode, allowed
  tool rules, hooks, and audit events before they touch the workspace.
- **Extensible shell:** slash commands, skills, hooks, MCP, plugins, background
  jobs, and IDE bridge experiments are part of the same command surface.

## Project Status

Codog is usable for local experimentation and day-to-day agent workflow testing.
The core prompt, REPL, TUI, provider streaming, tool execution, session, config,
and extension surfaces are implemented. Compatibility work is still active, so
treat advanced features as evolving unless you have validated them in your own
environment.

| Area | Status |
| --- | --- |
| One-shot prompts, REPL, TUI | Implemented |
| Anthropic-compatible streaming and provider routing | Implemented |
| Bash, read, write, edit, grep, glob, git, notebook, and code-intel tools | Implemented |
| Permissions, audit events, hooks, and sandbox toggles | Implemented, not a hard security boundary |
| JSONL sessions, resume, rewind, summaries, export, usage, and compaction | Implemented |
| Skills, slash commands, MCP, plugins, and background tasks | Implemented and expanding |
| IDE bridge, remote sessions, OAuth, updater, enterprise policy | Experimental |

## Quick Start

Codog requires Go 1.24.2 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

Set a provider credential, then ask from inside a repository:

```sh
export ANTHROPIC_API_KEY=<key>
codog -p "summarize this repository"
```

For longer work, use an interactive surface instead of packing everything into
one prompt:

```sh
codog repl
codog tui
```

Resume recent work when you want the agent to continue from local session
history:

```sh
codog --resume latest -p "continue the refactor"
```

Run `codog help` for the full command reference.

## Core Workflow

A typical Codog turn follows the same loop:

1. Load project instructions, config, focused files, and optional session
   history.
2. Send a provider request and stream the assistant response.
3. Parse requested tool calls from the model output.
4. Check permissions, allowed tools, hooks, and local safety rules.
5. Execute approved tools and append the result to the session log.
6. Continue until the model finishes, a limit is reached, or the user stops the
   run.

This design keeps the user-facing CLI, provider adapter, permission layer, and
session ledger close enough to inspect together.

## Everyday Use

Use `codog -p` for a bounded question or one-shot change. Use `codog repl` or
`codog tui` when the work needs several turns, approvals, or context updates.

The most common supporting surfaces are:

- `codog status` and `codog doctor` for environment and configuration checks.
- `codog sessions`, `codog history`, `codog summary`, and `codog export` for
  session review.
- `codog permissions` and `codog allowed-tools` for local execution control.
- `codog skills`, `codog commands`, `codog hooks`, `codog mcp`, and
  `codog plugins` for extension work.
- `codog usage`, `codog cost`, `codog compact`, and `codog cache` for tracking
  and managing context.

## Configuration

Configuration is layered so project defaults, personal preferences, and local
secrets stay separate.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions loaded into the agent context |
| `.codog.json` | Shared project configuration |
| `.codog.local.json` | Uncommitted local overrides |
| `.codog/commands` | Project slash commands |
| `.codog/skills` | Project skills |
| `.codog/hooks` | Project hook scripts |

Provider credentials are normally supplied through environment variables:

- `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`
- `OPENAI_API_KEY` and `OPENAI_BASE_URL`
- `XAI_API_KEY`
- `DASHSCOPE_API_KEY`
- `OLLAMA_HOST`

Keep secrets in environment variables or local-only config. Do not commit API
keys, generated sessions, local cache paths, or private credentials.

## Safety Model

Codog separates model intent from local execution. A model can request a tool,
but the runtime decides whether that request is allowed under the current
permission mode, workspace, policy, and hook configuration.

Common permission modes:

- `read-only` for inspection and planning.
- `workspace-write` for edits inside the current workspace.
- `prompt` for approval before sensitive actions.
- `danger-full-access` or `allow` for explicitly unrestricted local use.

These checks are guardrails for development workflow safety. They reduce
accidental damage, but they are not a complete sandbox for hostile repositories
or untrusted commands.

## Repository Map

| Path | Responsibility |
| --- | --- |
| `cmd/codog` | CLI entry point |
| `internal/agent` | Command dispatch, runtime wiring, and agent loop |
| `internal/anthropic` | Anthropic-compatible client and message types |
| `internal/tools` | Workspace tools for shell, files, search, git, and edits |
| `internal/session` | JSONL transcripts, resume, export, and metadata |
| `internal/config` | Layered user, project, local, and environment config |
| `internal/tui` | Bubble Tea interactive interface |
| `internal/mcp` | MCP client integration |
| `internal/mcpserver` | MCP server surface |
| `internal/skills` | Skill discovery and activation |
| `internal/hooks` | Local automation hooks |
| `internal/control` | Remote control and IDE bridge HTTP surface |

## Development

Build and test from the repository root:

```sh
go build ./cmd/codog
go test ./...
```

The test suite includes offline clients and compatibility harnesses, so most
CLI, session, tool, and extension behavior can be exercised without calling a
real model provider.

When contributing, keep changes portable. Avoid committing local absolute paths,
generated caches, API keys, machine-specific setup snippets, or AI-tool
attribution text.

## License

Codog is released under the [MIT License](LICENSE).
