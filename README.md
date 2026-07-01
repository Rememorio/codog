# Codog

Codog is a Go-native coding-agent CLI for repository work: ask a question,
make an edit, inspect a diff, resume a session, or hand the same runtime to a
TUI, MCP server, hook, or automation.

It follows the broad shape of Claude Code while keeping the implementation
local-first, auditable, and easy to ship as a single binary. Codog is
independent software and is not affiliated with Anthropic.

> Status: Codog is pre-1.0. The core loop works, but command names,
> configuration schema, and advanced compatibility behavior can still change.

## Why Codog

Most coding agents are useful but hard to inspect. Codog optimizes for a
smaller, Go-native runtime with clear boundaries:

- one binary for the CLI, REPL, TUI, local tools, sessions, MCP, hooks, and
  extension surfaces;
- explicit permission modes before tools read, write, edit, search, or run
  commands in a workspace;
- durable JSONL sessions that can be resumed, exported, compacted, inspected,
  and used by automation;
- package-level separation for provider IO, workspace IO, permissions, hooks,
  MCP, plugins, sessions, and terminal UI.

The goal is not to clone every surface at once. The goal is a coding-agent
runtime that is practical to use locally and practical to audit as it grows.

## Quick Start

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
codog -p "summarize this repository"
```

For a checkout:

```bash
go build ./cmd/codog
./codog repl
```

Common entry points:

- `codog -p "..."` runs one prompt and exits.
- `codog repl` starts an interactive repository session.
- `codog tui` opens the Bubble Tea terminal UI.
- `codog doctor` checks auth, config, git, hooks, MCP, and local runtime state.

Use `codog help` for command help and `codog capabilities --json` when another
tool needs the machine-readable surface area.

## What Works Today

Codog already includes the main pieces needed for local agent work:

| Area | Highlights |
| --- | --- |
| Interfaces | One-shot prompts, side questions, REPL, Bubble Tea TUI, slash commands. |
| Providers | Anthropic-compatible streaming and OpenAI-compatible provider configuration. |
| Workspace tools | Read, write, edit, multi-edit, grep, glob, bash, git, todo, diagnostics, context, and review helpers. |
| Permissions | Read-only, workspace-write, prompt, allow rules, broad-CWD guards, audit events, and undo snapshots. |
| Sessions | JSONL history, resume, list/show/fork/delete, export/share/copy, rewind, compaction, cost, usage, metrics, and insights. |
| Extensibility | Repository instructions, output styles, custom commands, prompt templates, skills, hooks, MCP, and plugins. |
| Automation | Local API, background tasks, cron, team workers, GitHub PR helpers, editor bridge surfaces, and updater scaffolding. |

Some larger surfaces are intentionally still rough: deep IDE behavior, remote
collaboration, marketplace UX, enterprise policy, sandbox parity, and complete
Claude Code compatibility all need more hardening before a stable release.

## Day-To-Day Flow

A typical Codog session stays close to the repository:

1. Open a project and set the permission mode you want.
2. Ask from a one-shot prompt, the REPL, or the TUI.
3. Review and approve tool actions when needed.
4. Let Codog edit files, run checks, inspect git state, or update todos.
5. Resume, export, compact, or rewind the JSONL session later.

Codog does not require a database or hosted service for this loop. Session
state and project-local customization stay on disk.

## Configuration

Configuration is layered from broad defaults to local overrides:

1. user config in the Codog config home;
2. project config in `.codog.json`;
3. uncommitted local overrides in `.codog.local.json`;
4. environment variables and CLI flags.

Minimal project config:

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

Provider credentials can come from environment variables such as
`ANTHROPIC_API_KEY`, from config files, or from `codog api-key`.

## Safety Model

Codog treats repository access as sensitive local automation.

- `read-only` is for inspection, explanation, and review.
- `workspace-write` allows edits inside the selected workspace.
- `prompt` asks before risky tool actions.
- `danger-full-access` is explicit and should be reserved for trusted local
  workflows.

Permission rules, command validation, audit logs, undo snapshots, and workspace
scope checks are designed to make tool execution visible and recoverable.

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

These extension points keep project-specific behavior close to the project
instead of hiding it in a global agent profile.

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
| `internal/control`, `internal/bridge` | Local API, remote control, and editor bridge surfaces. |

## Development

The normal validation loop is intentionally short:

```bash
go test ./...
go build ./cmd/codog
```

Keep generated local state, machine-specific paths, cache locations, and
tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
