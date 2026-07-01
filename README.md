# Codog

Codog is a Go-native coding-agent CLI for working on a repository from the
terminal. It is designed around a Claude Code-like workflow, but keeps the
runtime local-first, inspectable, and easy to distribute as a single binary.

Codog is independent software and is not affiliated with Anthropic. Claude Code
compatibility is a product direction, not a guarantee that every behavior or
edge case is identical today.

## Status

Codog is pre-1.0. The core repository workflow is usable: one-shot prompts,
interactive sessions, streaming model output, local tools, permission checks,
JSONL session history, configuration, MCP, hooks, skills, plugins, and a TUI
surface are present in the codebase.

Expect the compatibility layer, sandboxing behavior, editor integration, and
larger team/enterprise surfaces to keep changing while the project matures.

## Install

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

From a checkout:

```bash
go build ./cmd/codog
```

Set a model provider key before asking Codog to call a remote model:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

OpenAI-compatible providers can be configured with `OPENAI_API_KEY`,
`CODOG_PROVIDER`, `CODOG_MODEL`, CLI flags, or config files.

## Quick Start

Ask a question without allowing writes:

```bash
codog --permission-mode read-only prompt "summarize this repository"
```

Open an interactive agent session:

```bash
codog repl
```

Use the terminal UI:

```bash
codog tui
```

For the full command surface, use the binary itself:

```bash
codog help
codog capabilities --json
codog doctor
```

## How It Works

Codog keeps the agent loop small and explicit:

1. Load configuration, repository instructions, workspace context, and session
   state.
2. Stream a provider response.
3. Run approved tools for reading, writing, editing, searching, shell commands,
   todos, MCP calls, background jobs, or repository review.
4. Append model and tool events to local JSONL session files.

The main pieces stay separated: provider IO, workspace IO, permission decisions,
session storage, hooks, MCP, and terminal UI code live behind different package
boundaries. That makes the project easier to audit and easier to replace piece
by piece.

## What Codog Supports

Codog is not just a prompt wrapper. The current codebase includes:

- one-shot prompts, side questions, REPL, and Bubble Tea TUI entry points;
- Anthropic-compatible streaming plus OpenAI-compatible provider configuration;
- local file, search, edit, bash, git, todo, diagnostics, and context tools;
- permission modes, allow/deny rules, audit events, undo snapshots, and broad
  workspace guards;
- JSONL sessions with resume, history, export, summary, rewind, compact, cost,
  usage, and metrics commands;
- repository instructions, slash commands, skills, hooks, output styles,
  templates, MCP client/server support, and plugin loading;
- review, issue, PR, GitHub comments, background task, cron, and setup helper
  surfaces.

The exact set changes quickly. `codog capabilities --json` is the source of
truth for automation and compatibility checks.

## Configuration

Configuration is layered from broad defaults to local overrides:

1. user config in the Codog config home;
2. project config in `.codog.json`;
3. uncommitted local overrides in `.codog.local.json`;
4. environment variables and CLI flags.

A small project config might look like this:

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

Run `codog config paths` to see which files are active in a workspace.

## Safety Model

Repository access is treated as sensitive by default.

- `read-only` is for explanation, review, and inspection.
- `workspace-write` allows edits inside the selected workspace.
- `prompt` asks before risky actions.
- `danger-full-access` is explicit and should be reserved for trusted local
  workflows.

Permission rules, command validation, audit logs, undo snapshots, and workspace
scope checks are intended to make local automation inspectable instead of
opaque.

## Extending Codog

Codog can be shaped by files that live with the repository:

- instruction files such as `AGENTS.md`, `CLAUDE.md`, `CLAW.md`, and
  `.codog/instructions.md`;
- slash commands backed by prompts or local workflows;
- Markdown skills with optional tool constraints;
- hooks for agent, tool, command, session, and notification events;
- MCP servers and local MCP resources;
- plugins that bundle commands, skills, agents, hooks, MCP servers, and tools.

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
| `internal/control`, `internal/bridge` | Remote control and editor bridge surfaces. |

## Development

The usual validation loop is:

```bash
go test ./...
go build ./cmd/codog
```

Keep generated local state, machine-specific paths, cache locations, and
tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the MIT License.
