# Codog

`codog` is a Go-native coding-agent CLI for repository work. It can answer
questions, edit files, run workspace tools behind permission checks, and resume
previous sessions from JSONL history.

Codog follows the broad workflow shape of Claude Code while staying independent,
local-first, and easy to ship as a single binary. It is not affiliated with
Anthropic.

> Codog is pre-1.0. The core local workflow is usable, but command names,
> configuration fields, and advanced compatibility behavior may still change.

## What Codog Is

Codog is meant to be a small, inspectable runtime for coding-agent workflows:

- **Local by default**: project state, configuration, and session history live
  on disk.
- **Permissioned by default**: reads, writes, edits, searches, and shell
  commands go through explicit policy checks.
- **One runtime, several surfaces**: the same agent core powers one-shot
  prompts, REPL sessions, a terminal UI, slash commands, hooks, MCP, and local
  automation.
- **Auditable growth path**: provider IO, workspace tools, sessions,
  permissions, hooks, MCP, plugins, and UI code are separated into focused Go
  packages.

## Install

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

Set a provider key before the first real agent run:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

From a source checkout, build the local binary with:

```bash
go build ./cmd/codog
```

## First Run

Start with the smallest loop:

```bash
codog -p "summarize this repository"
```

For an interactive session:

```bash
codog repl
```

To inspect local readiness:

```bash
codog doctor
```

Use `codog help` for command-level help and `codog capabilities --json` when
another tool needs a machine-readable view of the supported surface area.

## How It Feels To Use

A normal Codog session stays close to the repository:

1. Open a project and choose the permission mode you want.
2. Ask from a one-shot prompt, the REPL, or the terminal UI.
3. Review requested tool actions when policy requires confirmation.
4. Let Codog read, edit, search, run checks, inspect git state, or update todos.
5. Resume, export, compact, rewind, or inspect the JSONL session later.

There is no required hosted service for this loop. Codog can be wired into
remote or team workflows, but the base experience is deliberately local.

## Capabilities

| Area | What Codog provides |
| --- | --- |
| Agent interfaces | One-shot prompts, side questions, REPL, Bubble Tea TUI, slash commands, and local help metadata. |
| Providers | Anthropic-compatible streaming plus OpenAI-compatible provider configuration. |
| Workspace tools | File read/write/edit, multi-edit, grep, glob, bash, git helpers, diagnostics, todo tracking, and review helpers. |
| Safety | Permission modes, allow/deny rules, risky-command validation, workspace scope checks, audit events, and undo snapshots. |
| Sessions | JSONL history, resume, list/show/fork/delete, export/share/copy, rewind, compaction, cost, usage, metrics, and insights. |
| Extensibility | Repository instructions, output styles, custom commands, prompt templates, skills, hooks, MCP clients/servers, and plugins. |
| Automation | Local API surfaces, background tasks, cron-style jobs, team workers, editor bridges, and updater scaffolding. |

Some larger surfaces still need hardening before a stable release: deep IDE
behavior, remote collaboration, multi-agent orchestration, marketplace UX,
enterprise policy, cross-platform sandboxing, and full Claude Code parity.

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

Provider credentials can come from environment variables, config files, or
`codog api-key`.

## Project Conventions

Codog reads project-local files so a repository can describe how it should be
worked on:

- instruction files such as `AGENTS.md`, `CLAUDE.md`, `CLAW.md`, and
  `.codog/instructions.md`;
- Markdown slash commands and prompt templates;
- Markdown skills with optional tool constraints;
- hooks for agent, tool, command, session, notification, and background events;
- MCP servers, local MCP resources, and MCP prompt definitions;
- plugins that bundle commands, skills, agents, hooks, MCP servers, tools, and
  marketplace metadata.

These files keep project-specific behavior close to the codebase instead of
hiding it in a global agent profile.

## Development

The normal validation loop is intentionally short:

```bash
go test ./...
go build ./cmd/codog
```

The most relevant package boundaries are:

| Path | Responsibility |
| --- | --- |
| `cmd/codog` | CLI entry point. |
| `internal/agent` | Agent loop, command dispatch, slash handling, and local tools. |
| `internal/config` | Config loading, merging, validation, and persistence. |
| `internal/anthropic` | Anthropic-compatible streaming client. |
| `internal/session` | JSONL session storage, resume, export, and compaction support. |
| `internal/mcp`, `internal/mcpserver` | MCP client and server support. |
| `internal/tui` | Bubble Tea terminal UI. |
| `internal/control`, `internal/bridge` | Local API, remote control, and editor bridge surfaces. |

Keep generated local state, machine-specific paths, cache locations, and
tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
