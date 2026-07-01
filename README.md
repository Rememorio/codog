# Codog

Codog is a Go-native coding agent for terminal-based repository work. It wraps
model streaming, workspace-aware tools, resumable sessions, local configuration,
and permission checks into a single binary.

The project is inspired by Claude Code-style workflows, but it is an
independent implementation. It is not affiliated with Anthropic and is not a
drop-in replacement for Claude Code.

## What Codog Is

Codog is meant for developers who want a coding-agent runtime they can inspect,
debug, and extend as ordinary Go code.

Use it to:

- ask one-off questions about a repository;
- iterate in a REPL or terminal UI;
- let the agent read, search, edit, and test files inside a workspace;
- resume prior JSONL sessions with their local context intact;
- experiment with skills, hooks, MCP, plugins, policies, and bridge surfaces
  without depending on a hidden service.

The design favors local files, explicit permissions, and small runtime
boundaries over opaque background state.

## Project Status

Codog is pre-1.0. The core local workflow is usable, but command names,
configuration fields, compatibility behavior, and extension APIs may still
change.

It is currently a good fit for local experimentation and active feature
development. It should not yet be treated as a mature enterprise agent platform
or a complete Claude Code clone.

For the authoritative machine-readable inventory of commands, tools, protocols,
and feature flags, run:

```bash
codog capabilities --json
```

## Install

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

Set an Anthropic-compatible API key before using the default provider:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

## First Run

Run Codog from the repository you want it to inspect.

```bash
codog -p "summarize this repository"
```

Use an interactive mode when the task needs multiple turns:

```bash
codog repl
codog tui
```

The README intentionally stays short. Use `codog --help` for the human command
surface and `codog capabilities --json` for automation.

## How It Works

Each turn moves through a local pipeline:

1. load project instructions, config, memory, focused files, slash commands,
   skills, hooks, MCP endpoints, plugins, and session history;
2. assemble a provider request and stream the response;
3. route tool calls through workspace checks, permission rules, policy checks,
   sandboxing, and hooks;
4. append messages, tool calls, approvals, token usage, cost data, summaries,
   and compaction metadata to a resumable JSONL session.

Most runtime state is plain text or JSON so it can be reviewed, diffed, backed
up, or deleted without special tooling.

## Safety Model

Codog treats tool execution as a local runtime concern, not just a prompt
convention.

- Workspace boundaries keep file operations scoped to the selected project.
- Permission modes and approval rules control which tools can run.
- Shell commands can pass through validation, confirmation, and sandbox
  strategies where the platform supports them.
- Hooks and policy checks provide extra gates for teams that want stricter
  behavior.
- Session files record what happened so prior work can be audited or resumed.

## Project Files

Codog reads these repository-local files when present:

| Path | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions for the agent. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Uncommitted local overrides. |
| `.codog/commands` | Markdown slash commands. |
| `.codog/templates` | Reusable prompt templates. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Local automation hooks. |
| `.codog/plugins` | Plugin manifests and packaged extension content. |

Compatible `.claude` instruction, command, and skill locations are loaded where
that compatibility is useful and implemented.

## Architecture

The CLI entry point is intentionally thin. Most behavior lives in focused
packages under `internal/`:

| Package | Responsibility |
| --- | --- |
| `agent` | Turn orchestration, session flow, command handling, and output. |
| `anthropic` and provider packages | Streaming model protocol adapters. |
| `tools` | Permissioned workspace tools and Claude-style aliases. |
| `config`, `session`, `hooks`, `skills`, `mcp`, `plugins` | Local runtime state and extension surfaces. |
| `sandbox`, `policyengine`, `bridge`, `workers`, `tui`, `updater` | Larger automation, safety, UI, and integration layers. |

## Compatibility

Codog mirrors Claude-style names, file locations, commands, and protocols when
they make local development smoother. Compatibility is practical rather than
bug-for-bug: Go-native implementation quality and inspectable behavior take
priority over copying private implementation details.

Use `codog capabilities --json` when you need to check whether a compatibility
surface is present in the current build.

## Development

For a local checkout:

```bash
go test ./...
go build ./cmd/codog
```

Keep generated state, machine-specific paths, local cache locations, secrets,
and tool attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
