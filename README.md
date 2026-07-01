# Codog

Codog is a Go-native coding agent CLI: one binary that can talk to LLM
providers, inspect and edit a workspace, run commands behind permission gates,
and keep resumable local sessions.

It is a clean-room, Claude Code-like implementation built around public API
contracts and Codog's own runtime design. It is not affiliated with Anthropic,
and exact Claude Code parity is still an active compatibility target.

## Status

Codog is usable for local development workflows, but the project is still
experimental. Interfaces, config keys, and some advanced integration behavior
may change while the implementation converges.

For the most accurate view of a build, prefer the runtime checks:

```bash
codog doctor
codog self-test
codog capabilities --json
```

## Why Codog

- **Single Go binary** with prompt, REPL, and terminal UI entry points.
- **Local-first sessions** stored as JSONL, with resume, export, summaries,
  rewind, and compaction.
- **Explicit permissions** for shell commands and file tools, from read-only to
  full access.
- **Provider flexibility** through Anthropic Messages streaming and
  OpenAI-compatible streaming models.
- **Repo-native customization** with skills, slash commands, hooks, templates,
  MCP servers, agents, and plugins.

## Install

Install from source with Go 1.24+:

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

Or build from a checkout:

```bash
go build ./cmd/codog
```

Set an API key for the provider you want to use:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

Then run a first request:

```bash
codog prompt "summarize this repository"
```

## Use Codog

| Workflow | Entry point | When to use it |
| --- | --- | --- |
| One-shot task | `codog prompt "..."` | Ask a question, review a diff, or make a focused change. |
| Interactive session | `codog repl` | Keep context open while you iterate. |
| Terminal UI | `codog tui` | Compose prompts and inspect state in a Bubble Tea interface. |
| Resume work | `codog --resume latest repl` | Continue the most recent JSONL session. |
| Conservative review | `codog --permission-mode read-only prompt "..."` | Let the agent inspect without writing files or running mutating tools. |

Inside interactive sessions, use slash commands such as `/help`, `/model`,
`/permissions`, `/compact`, `/tools`, `/commands`, and `/skills`.

## Capabilities

Codog groups its behavior around the jobs a coding agent needs to perform:

| Area | What is implemented |
| --- | --- |
| Agent loop | Streaming responses, one-shot prompts, REPL, TUI, slash commands, prompt history, and structured output modes. |
| Workspace tools | Bash, read, write, edit, grep, glob, git helpers, todos, tasks, background jobs, notebooks, and lightweight code intelligence. |
| Session state | JSONL persistence, resume, rewind, export, summaries, token and cost accounting, context inspection, and auto-compaction. |
| Safety | Permission modes, allow/deny rules, broad-directory guards, command validation, undo snapshots, and audit events. |
| Memory and context | Project instructions from `AGENTS.md`, `CLAUDE.md`, `.claude/CLAUDE.md`, `CLAW.md`, and `.codog/instructions.md`, plus focused paths and `@file` references. |
| Extensibility | Markdown skills, custom commands, templates, hooks, MCP client/server support, local plugins, and plugin marketplace metadata. |
| Integrations | ACP/Zed bridge, editor bridge, remote HTTP control, shell integration, GitHub workflow generation, updater, and handoff reports. |

Run `codog capabilities` for the complete machine-readable list exposed by the
current build.

## Permissions

Codog does not need full access by default. Choose the narrowest mode that fits
the task:

```bash
codog --permission-mode read-only prompt "review this change"
codog --permission-mode prompt prompt "fix the failing test"
codog --allowed-tools "Read" --allowed-tools "Bash(go test:*)" prompt "verify"
```

Supported modes include `read-only`, `workspace-write`, `danger-full-access`,
`prompt`, and `allow`. Compatibility aliases such as `Bash`, `Read`, `Write`,
`Edit`, `GrepSearch`, `GlobSearch`, `Task`, and `TodoWrite` are accepted while
Codog keeps canonical tool names internally.

## Configuration

Configuration is merged from user config, project config, local overrides,
environment variables, and flags:

- `~/.codog/config.json`
- `.codog.json`
- `.codog.local.json`
- environment variables such as `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`,
  `CODOG_MODEL`, `CODOG_PERMISSION_MODE`, and `CODOG_CONFIG_HOME`

A small project config can look like this:

```json
{
  "model": "claude-sonnet-4-5",
  "permission_mode": "workspace-write",
  "max_turns": 8,
  "max_tokens": 4096,
  "additional_dirs": ["../shared"],
  "permission_rules": {
    "deny": ["bash:rm -rf"]
  }
}
```

Common provider setup commands:

```bash
codog providers set anthropic
codog providers set openai --model openai/gpt-4o-mini
codog providers set custom --base-url http://127.0.0.1:8000 --model openai/local
```

Use `codog config`, `codog config paths`, and `codog help config` when you need
the full config surface.

## Extension Points

Codog looks for user and workspace extensions under `~/.codog`, `.codog`, and
compatible `.claude` directories:

- `skills/` for Markdown skills with metadata and optional allowed tools.
- `commands/` for custom slash commands and reusable workflows.
- `templates/` for parameterized prompts.
- `hooks/hooks.json` and config `hooks` for command, HTTP, prompt, and agent
  hooks.
- plugin directories containing commands, skills, agents, hooks, MCP servers,
  and local tools.

MCP works both ways: Codog can call configured stdio MCP servers, and
`codog mcp serve` exposes Codog's local tools over stdio MCP.

## Project Layout

| Path | Purpose |
| --- | --- |
| `cmd/codog` | CLI entry point. |
| `internal/agent` | Command surface, agent orchestration, tools, and slash command handling. |
| `internal/config` | Config loading, merging, validation, and persistence helpers. |
| `internal/anthropic` | Anthropic-compatible streaming client. |
| `internal/session` | JSONL session storage and resume support. |
| `internal/mcp` and `internal/mcpserver` | MCP client and server implementations. |
| `internal/control`, `internal/bridge` | Remote control and editor bridge surfaces. |

## Command Reference

The README is an orientation document, not the command manual. Use the built-in
help so the reference always matches the binary you are running:

```bash
codog help
codog help prompt
codog commands list
codog skills list
codog capabilities
```

## Development

Codog targets Go 1.24+. The usual validation loop is:

```bash
go test ./...
go build ./cmd/codog
```

Generated local state is written under `.codog/` and is ignored by default.

## License

Codog is released under the MIT License.
