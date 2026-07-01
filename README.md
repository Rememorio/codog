# Codog

Codog is a Go-native coding-agent CLI for working inside a repository. It aims
to keep the Claude Code-style workflow familiar while making the runtime
local-first, inspectable, and easy to ship as a single binary.

Codog is independent software and is not affiliated with Anthropic. Claude
Code compatibility is a product direction, not a promise that every behavior is
identical today.

## Why Codog

Most coding agents hide a lot of important behavior behind a large runtime.
Codog takes the opposite shape:

- one binary for the main CLI, REPL, TUI, local tools, sessions, and extension
  surfaces;
- explicit permission modes before tools read, write, edit, search, or run
  commands in a workspace;
- durable JSONL sessions that can be resumed, exported, compacted, inspected,
  and used by automation;
- Go package boundaries for provider IO, workspace IO, permissions, hooks,
  MCP, plugins, sessions, and terminal UI.

The result is a coding-agent runtime that should be practical to use locally
and practical to audit as it grows.

## Current Status

Codog is pre-1.0. The repository already contains the core agent loop, one-shot
prompts, REPL, Bubble Tea TUI, Anthropic-compatible streaming,
OpenAI-compatible provider configuration, local tools, permission checks,
JSONL session storage, slash commands, hooks, skills, MCP, plugin loading, and
several repository workflow helpers.

Some advanced compatibility, sandboxing, editor, remote, multi-agent, and
enterprise surfaces are still evolving. Treat command output and configuration
schemas as unstable until the project cuts a 1.0 release.

## Quick Start

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
codog -p "summarize this repository"
```

For a checked-out copy:

```bash
go build ./cmd/codog
./codog repl
```

Useful entry points after installation:

- `codog repl` starts an interactive repository session.
- `codog tui` opens the terminal UI.
- `codog doctor` checks auth, config, git, hooks, MCP, and local runtime state.
- `codog capabilities --json` reports the machine-readable command, tool, and
  feature surface for this build.

## Core Workflow

Codog keeps the agent loop deliberately small:

1. Load configuration, repository instructions, selected workspace context, and
   session state.
2. Stream a model response from the configured provider.
3. Execute approved tools for files, search, edits, shell commands, git, todos,
   MCP, hooks, reviews, or background work.
4. Append model and tool events to a local JSONL session.

That loop is the same whether the user starts from a one-shot prompt, a REPL
session, the TUI, a slash command, or a local automation surface.

## Capability Map

| Area | What is in the codebase today |
| --- | --- |
| Agent interfaces | One-shot prompts, side questions, REPL, Bubble Tea TUI, slash commands. |
| Model providers | Anthropic-compatible streaming and OpenAI-compatible configuration. |
| Workspace tools | Read, write, edit, multi-edit, grep, glob, bash, git, todo, diagnostics, context, and review helpers. |
| Permissions | Read-only, workspace-write, prompt, allow rules, broad-CWD guards, audit events, and undo snapshots. |
| Sessions | JSONL history, resume, list/show/fork/delete, export/share/copy, rewind, compaction, cost, usage, and metrics. |
| Customization | Repository instructions, output styles, custom commands, prompt templates, skills, hooks, and plugins. |
| Integrations | MCP client/server support, GitHub-oriented helpers, editor bridge surfaces, local API, background tasks, cron, and updater scaffolding. |

The README intentionally does not mirror every CLI flag. Use `codog help` for
human help and `codog capabilities --json` for automation.

## Configuration

Configuration is layered from broad defaults to local overrides:

1. user config in the Codog config home;
2. project config in `.codog.json`;
3. uncommitted local overrides in `.codog.local.json`;
4. environment variables and CLI flags.

A small project config can be enough:

```json
{
  "model": "claude-sonnet-4-5",
  "permission_mode": "workspace-write",
  "max_turns": 8,
  "max_tokens": 4096,
  "permission_rules": {
    "deny": ["bash:rm -rf"]
  }
}
```

Provider keys can be supplied through environment variables such as
`ANTHROPIC_API_KEY` or `OPENAI_API_KEY`, through config files, or through the
CLI configuration commands.

## Safety Model

Codog treats repository access as sensitive local automation.

- `read-only` is for inspection, explanation, and review.
- `workspace-write` allows edits inside the selected workspace.
- `prompt` asks before risky tool actions.
- `danger-full-access` is intentionally explicit and should be reserved for
  trusted local workflows.

Permission rules, command validation, audit logs, undo snapshots, and workspace
scope checks are meant to make tool execution visible and recoverable.

## Extending Codog

Codog can be shaped by files that live with a repository:

- instruction files such as `AGENTS.md`, `CLAUDE.md`, `CLAW.md`, and
  `.codog/instructions.md`;
- Markdown slash commands and prompt templates;
- Markdown skills with optional tool constraints;
- hooks for agent, tool, command, session, notification, and background events;
- MCP servers, local MCP resources, and MCP prompt definitions;
- plugins that bundle commands, skills, agents, hooks, MCP servers, tools, and
  marketplace metadata.

These extension points are designed to keep project-specific behavior close to
the project instead of burying it in a global agent profile.

## Repository Layout

| Path | Purpose |
| --- | --- |
| `cmd/codog` | CLI entry point. |
| `internal/agent` | Agent loop, command dispatch, slash handling, and local tools. |
| `internal/config` | Config loading, merging, validation, and persistence. |
| `internal/anthropic` | Anthropic-compatible streaming client. |
| `internal/session` | JSONL session storage, resume, export, and compaction support. |
| `internal/mcp`, `internal/mcpserver` | MCP client and server support. |
| `internal/tui` | Bubble Tea terminal UI. |
| `internal/control`, `internal/bridge` | Local API, remote-control, and editor bridge surfaces. |

## Development

The normal validation loop is short:

```bash
go test ./...
go build ./cmd/codog
```

Keep generated local state, machine-specific paths, cache locations, and
tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
