# Codog

Codog is a Go-native coding agent CLI for local software projects.

It is built around one practical idea: a terminal agent should be easy to run,
easy to inspect, and conservative about local changes. Codog packages the core
agent loop, provider streaming, workspace tools, permissions, and durable
session state into a single Go binary.

## Why Codog Exists

Modern coding agents are useful, but their behavior can be hard to audit. Codog
explores the same workflow with an implementation that is local-first and
inspectable:

- A single binary for normal CLI use.
- Plain local files for config, memory, sessions, hooks, and audit records.
- Explicit permission checks before shell commands or workspace writes.
- Provider-backed streaming with Anthropic, OpenAI-compatible, xAI/Grok, and
  DashScope-compatible endpoints.
- Compatibility-minded command contracts for sessions, slash commands, tools,
  MCP, skills, hooks, plugins, and editor-facing workflows.

Codog is not trying to hide state in a service. It is trying to make the moving
parts of a coding agent visible enough that you can debug and extend them.

## Quick Start

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

Set a provider credential:

```sh
export ANTHROPIC_API_KEY=<key>
```

Then try one of the main entry points:

```sh
codog -p "explain this repository"
codog repl
codog tui
codog doctor
```

Use `codog --help` for the full CLI surface and `codog capabilities` for a
machine-readable report of the current build.

## How It Works

A Codog run follows the same shape whether it starts from a one-shot prompt,
the REPL, or the TUI:

1. Load repository instructions, config layers, memory, focused paths, and
   optional session history.
2. Stream a model response through the configured provider.
3. Route tool calls through the local registry and permission model.
4. Persist messages, tool calls, approvals, usage, summaries, and metadata in
   JSONL session state.

The default posture is useful for local development, but you can make it more
restrictive when exploring an unfamiliar repository:

```sh
codog --permission-mode read-only -p "map the project and identify its tests"
```

## Capabilities

Codog's current surface is broad, but not every area has the same depth. Treat
command-specific help and `codog capabilities` as the exact contract for the
build you are running.

| Area | Current shape |
| --- | --- |
| Agent loop | One-shot prompts, side questions, REPL, Bubble Tea TUI, streaming output, and resumable sessions. |
| Providers | Anthropic Messages streaming plus OpenAI-compatible chat-completions configuration for OpenAI-style, xAI/Grok, and DashScope-compatible models. |
| Workspace tools | Bash, read, write, edit, patch, grep, glob, git helpers, notebook helpers, and code-intelligence helpers. |
| Safety | Permission modes, allow and deny rules, broad working-directory guards, sandbox detection, approval records, and audit events. |
| State | JSONL sessions, prompt history, summaries, export, sharing, usage, cost, cache statistics, rewind, and compaction. |
| Extensibility | Slash commands, prompt templates, skills, hooks, MCP client/server pieces, plugins, and marketplace metadata. |
| Operations | Doctor/status reports, background tasks, scheduled prompts, recovery ledgers, updater checks, and policy reports. |
| Integrations | Editor/ACP bridge state, remote-control surfaces, OAuth profiles, GitHub workflow helpers, local agents, and worktree scaffolding. |

## Configuration

Configuration is layered so project defaults and personal preferences can stay
separate.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Repository guidance loaded into agent context. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Uncommitted local overrides. |
| `.codog/commands` | Project slash commands. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Project hooks and local automation. |

Secrets should live in environment variables or local config, not in committed
project files.

## Safety Model

Codog separates "the model suggested this" from "the machine executed this".
Tools declare required permissions, and the runtime checks the active mode and
rules before execution.

The main modes are:

- `read-only` for inspection and planning.
- `workspace-write` for edits inside the trusted workspace.
- `prompt` for interactive confirmation.
- `danger-full-access` and `allow` for explicitly unrestricted local use.

These controls are guardrails for development workflow. Do not treat them as a
hardened isolation boundary for untrusted code.

## Development

The CLI entry point is `cmd/codog`. Most behavior lives under `internal/`,
split into packages for the agent loop, providers, tools, sessions,
configuration, hooks, skills, plugins, MCP, background work, code intelligence,
and the TUI.

Common local checks:

```sh
go test ./...
go build ./cmd/codog
```

Keep generated state, secrets, machine-specific paths, and tool-attribution
signatures out of commits.

## License

Codog is released under the [MIT License](LICENSE).
