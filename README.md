# Codog

Codog is a Go-native coding agent CLI for working inside a repository. It runs
as a single binary, keeps session state in local files, and exposes the agent
loop through a terminal-first interface.

The project follows Claude Code-style conventions where they are useful for
local coding workflows, but it is an independent implementation and is not
affiliated with Anthropic.

Codog is pre-1.0. The core local workflow is usable, while command names,
configuration fields, compatibility behavior, and integration surfaces may
still change.

## Why Codog

Codog is for developers who want a coding agent that is easy to inspect and
extend in Go:

- a single executable instead of a long-running local service;
- explicit permission modes before tools read, edit, or execute in a workspace;
- resumable JSONL sessions that can be listed, exported, compacted, and
  inspected;
- repository-local instructions, commands, skills, hooks, MCP servers, and
  plugin metadata;
- a codebase organized around small internal packages rather than a large
  opaque runtime.

It is not yet the best fit if you need a polished hosted workflow, enterprise
fleet controls, marketplace distribution, deep IDE collaboration, or guaranteed
Claude Code parity today. Those areas are being modeled in the Go codebase, but
the strongest path is currently the local single-repository loop.

## Quick Start

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

Set an Anthropic-compatible credential with an environment variable or store it
through Codog:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
codog api-key set "sk-ant-..."
```

Run a one-shot prompt from the repository you want Codog to inspect:

```bash
codog -p "summarize this repository"
```

Start an interactive session when you want the agent to keep context:

```bash
codog repl
```

Use `codog doctor` for a local health check and `codog --help` for the full CLI
surface.

## Core Workflow

Codog starts from the current working directory and treats it as the active
workspace. Before each model turn it gathers project instructions, focused
files, session context, config, and tool metadata. Tool calls then pass through
workspace scope checks and permission policy before they touch the filesystem,
shell, or networked integrations.

The common loop is:

1. add project guidance in `AGENTS.md` or `.codog.json`;
2. ask a one-shot question with `codog -p` or enter `codog repl`;
3. approve or deny tool use according to the configured permission mode;
4. resume, export, compact, or inspect the JSONL session later.

## What It Supports

| Area | Capability |
| --- | --- |
| Agent loop | One-shot prompts, REPL, Bubble Tea TUI, Anthropic-compatible streaming, and stream JSON output. |
| Workspace tools | Read, write, edit, grep, glob, list files, run shell commands, inspect git, manage todos, and call MCP tools. |
| Safety | Workspace scoping, additional directories, permission modes, allowed and denied tools, shell validation, audit events, and hookable permission requests. |
| Sessions | JSONL storage, resume, fork, rename, rewind, compact, summarize, export, share, prompt history, usage, token, and cost reports. |
| Project context | `AGENTS.md`, Codog config files, compatible Claude instruction files, focused files, memories, prompt templates, and custom slash commands. |
| Extensions | MCP client and server helpers, skills, hooks, plugins, custom commands, output styles, background tasks, cron jobs, subagents, and verifier setup. |
| Integrations | Git and PR helpers, OAuth profiles, remote control bridge, ACP server, code intelligence hooks, updater plumbing, and mock parity harnesses. |

## Project Files

Codog reads a small set of repository-local files when present:

- `AGENTS.md` for agent instructions;
- `.codog.json` for shared project configuration;
- `.codog.local.json` for uncommitted local overrides;
- `.codog/commands` for Markdown slash commands;
- `.codog/templates` for reusable prompt templates;
- `.codog/skills` for project skills;
- `.codog/hooks` and `.codog/plugins` for local automation and extension
  metadata.

Compatible `.claude` instruction, command, and skill locations are also loaded
where the Go implementation supports them.

## Configuration

Configuration is layered from user defaults to project files, local overrides,
environment variables, and CLI flags. A small project config is usually enough:

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

Useful environment variables include `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`,
`ANTHROPIC_BASE_URL`, `CODOG_BASE_URL`, `CODOG_MODEL`, `CODOG_API_KEY`,
`CODOG_PERMISSION_MODE`, and `CODOG_CONFIG_HOME`.

## Development

Build and validate from a source checkout:

```bash
go test ./...
go build ./cmd/codog
```

The main packages are:

- `cmd/codog` for the CLI entry point;
- `internal/agent` for command dispatch and top-level workflows;
- `internal/runloop` for model turns and tool execution;
- `internal/anthropic` for the Anthropic-compatible client and protocol types;
- `internal/tools` for workspace tools, permissions, aliases, and MCP tool
  integration;
- `internal/session` for JSONL session storage and lifecycle operations;
- `internal/config` for layered configuration;
- `internal/tui`, `internal/mcp`, `internal/hooks`, `internal/plugins`, and
  `internal/bridge` for optional integration surfaces.

Keep generated state, machine-specific paths, local cache locations, secrets,
and tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
