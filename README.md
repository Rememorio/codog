# Codog

Codog is a Go-native coding agent CLI for working inside a repository. It runs as
a single binary, keeps state in local JSONL sessions, and exposes the agent loop
through a terminal-first workflow.

Codog follows Claude Code-style conventions where they are useful for local
coding, but it is an independent implementation and is not affiliated with
Anthropic.

> Codog is pre-1.0. The local repository workflow is usable, while command names,
> configuration fields, compatibility behavior, and integration surfaces may
> still change.

## Why Codog

Codog is built for developers who want a coding agent that is easy to inspect,
run, and extend in Go.

- **Single binary**: no required daemon for the normal local workflow.
- **Local-first state**: sessions, project guidance, memories, commands, skills,
  and hooks live in files you can inspect.
- **Explicit safety model**: tool calls pass through workspace scoping,
  permission modes, allow/deny rules, and optional hooks.
- **Terminal-native UX**: one-shot prompts, REPL, TUI, JSON output, and resumable
  sessions are first-class.
- **Compatibility-minded design**: familiar agent tools, slash commands, MCP,
  skills, hooks, and bridge surfaces are modeled without hiding the Go
  implementation behind a large opaque runtime.

Codog is not yet a polished hosted product. Enterprise fleet policy, deep IDE
collaboration, plugin marketplace distribution, remote multi-user sessions, and
complete Claude Code parity are still implementation areas rather than stable
guarantees.

## Install

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

Configure an Anthropic-compatible credential with an environment variable or the
local key helper:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
codog api-key set "sk-ant-..."
```

## First Session

Run Codog from the repository you want it to inspect.

```bash
codog -p "summarize this repository"
```

For an ongoing conversation:

```bash
codog repl
```

Useful first checks:

```bash
codog doctor
codog status
codog capabilities
```

Use `codog --help` or `codog capabilities --json` for the full command and tool
surface. The README intentionally stays at the workflow level instead of acting
as a generated command reference.

## How It Works

Codog treats the current directory as the active workspace. Before each model
turn, it assembles project instructions, focused files, memory, session context,
configuration, and available tools. Tool calls are then checked against the
workspace scope and permission policy before reading files, editing files,
running commands, or calling integrations.

The common loop is:

1. Add project guidance with `AGENTS.md` or `.codog.json`.
2. Ask a one-shot prompt with `codog -p` or enter `codog repl`.
3. Approve or deny tool use according to the configured permission mode.
4. Resume, inspect, compact, export, or share the JSONL session later.

## Capability Map

| Area | What Codog provides |
| --- | --- |
| Agent workflow | One-shot prompts, REPL, Bubble Tea TUI, streaming output, JSON output, prompt history, and session resume. |
| Workspace tools | Read, write, edit, grep, glob, shell, git, todo, notebook, code-intelligence, and MCP-backed tools. |
| Safety | Workspace scoping, additional directory grants, permission modes, allow/deny rules, shell validation, audit events, and permission hooks. |
| Project context | `AGENTS.md`, Codog config files, compatible Claude-style instruction locations, focused files, project memory, prompt templates, and custom slash commands. |
| Extensions | MCP client/server support, skills, hooks, plugins, local background tasks, cron prompts, workers, and bridge surfaces for editor or remote control experiments. |
| Observability | Status reports, health checks, usage and cost estimates, prompt cache stats, metrics, traces, and mock parity harnesses. |

## Project Files

Codog reads these repository-local files when present:

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

Common environment variables include `ANTHROPIC_API_KEY`,
`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CODOG_BASE_URL`, `CODOG_MODEL`,
`CODOG_API_KEY`, `CODOG_PERMISSION_MODE`, and `CODOG_CONFIG_HOME`.

## Development

Build and validate from a source checkout:

```bash
go test ./...
go build ./cmd/codog
```

The main package boundaries are:

- `cmd/codog`: CLI entry point;
- `internal/agent`: command dispatch and top-level workflows;
- `internal/runloop`: model turns and tool execution;
- `internal/anthropic`: Anthropic-compatible client and protocol types;
- `internal/tools`: workspace tools, permissions, aliases, and MCP integration;
- `internal/session`: JSONL session storage and lifecycle operations;
- `internal/config`: layered configuration;
- `internal/tui`, `internal/mcp`, `internal/hooks`, `internal/plugins`, and
  `internal/bridge`: optional integration surfaces.

Keep generated state, machine-specific paths, local cache locations, secrets,
and tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
