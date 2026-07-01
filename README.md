# Codog

`codog` is a Go-native coding agent CLI for repository work. It runs as a
single binary, keeps local state on disk, gates workspace tools behind explicit
permission policy, and stores sessions as JSONL so work can be resumed,
inspected, or exported later.

Codog follows Claude Code-style workflows where that makes the local developer
experience better, but it is an independent project and is not affiliated with
Anthropic.

> Status: pre-1.0. The core local workflow is usable, while command names,
> config fields, compatibility behavior, and advanced integrations may still
> change.

## Why Codog

- **One binary**: no runtime service is required for the normal local loop.
- **Repository-first**: prompts, tools, instructions, todos, and sessions stay
  close to the code being changed.
- **Permissioned tools**: file access, edits, shell commands, searches, network
  access, and automation can be allowed, denied, or prompted by policy.
- **Inspectable state**: sessions, undo snapshots, traces, metrics, and local
  config are plain files rather than opaque hosted state.
- **Compatibility-minded**: Claude Code conventions, MCP, hooks, skills,
  custom commands, and editor surfaces are modeled as Go packages instead of a
  single monolithic runtime.

## Quick Start

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
codog -p "summarize this repository"
```

For an interactive session, use `codog repl` or `codog tui`.

Useful discovery commands:

```bash
codog doctor
codog help
codog capabilities --json
```

From a source checkout, `go build ./cmd/codog` builds the local binary.

## Core Workflow

1. Start in a repository and choose a permission mode.
2. Ask from a one-shot prompt, the REPL, or the terminal UI.
3. Codog loads project instructions, config, focused files, and session context.
4. Tool calls are checked against workspace scope and permission rules.
5. The session is written as JSONL and can be resumed, summarized, compacted,
   rewound, exported, or inspected later.

The default path is local-first. Remote control, team workers, and editor
bridges exist as integration surfaces, but they are not required for day-to-day
single-repository work.

## Capability Map

| Area | Current surface |
| --- | --- |
| Interfaces | One-shot prompts, side questions, REPL, Bubble Tea TUI, slash commands, and machine-readable capability metadata. |
| Model IO | Anthropic-compatible streaming, model/base URL settings, API key storage, OAuth profile scaffolding, and provider status commands. |
| Workspace tools | Read, write, edit, multi-edit, grep, glob, ls, bash, PowerShell, git helpers, code-intel helpers, notebooks, todos, and review helpers. |
| Safety | Permission modes, allow/deny rules, risky-command validation, workspace scope checks, audit events, undo snapshots, and sandbox toggles. |
| Sessions | JSONL storage, resume, list/show/fork/delete/rename, history, summary, rewind, compact, export/share/copy, cost, usage, metrics, and insights. |
| Extensibility | Project instructions, output styles, custom commands, prompt templates, skills, hooks, MCP client/server support, and plugins. |
| Automation | Background tasks, cron jobs, team workers, local API surfaces, editor bridge hooks, remote setup controls, and updater primitives. |

This map describes the implemented surface area, not a stability guarantee.
Large surfaces such as deep IDE behavior, remote collaboration, multi-agent
orchestration, marketplace UX, enterprise policy, and cross-platform sandboxing
still need hardening before a stable release.

## Configuration

Configuration is layered from broad defaults to narrow overrides:

1. user config in the Codog config home;
2. project config in `.codog.json`;
3. uncommitted local overrides in `.codog.local.json`;
4. environment variables and CLI flags.

Example project config:

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

Credentials can be provided through environment variables, config files, or the
`api-key` command.

## Project Files

Codog can read project-local files so each repository can describe how it
should be worked on:

- instructions in `AGENTS.md`, `.codog/instructions.md`, and compatible Claude
  instruction locations;
- Markdown commands in `.codog/commands`, `.claude/commands`, or the user
  command directory;
- prompt templates in `.codog/templates` or the user template directory;
- skills in `.codog/skills`, `.claude/skills`, or the user skill directory;
- hooks, MCP servers, agents, tools, and plugin metadata under `.codog`.

The intent is to keep project-specific behavior versionable and reviewable
instead of hiding it in a personal prompt.

## Development

The normal validation loop is short:

```bash
go test ./...
go build ./cmd/codog
```

Key package boundaries:

| Path | Responsibility |
| --- | --- |
| `cmd/codog` | CLI entry point. |
| `internal/agent` | Agent loop, command dispatch, slash handling, and top-level command surfaces. |
| `internal/tools` | Tool definitions, permission classes, workspace actions, MCP tools, and compatibility aliases. |
| `internal/config` | Config loading, merging, validation, policy, and persistence. |
| `internal/anthropic` | Anthropic-compatible streaming client and protocol types. |
| `internal/session` | JSONL session storage, resume, export, and compaction support. |
| `internal/mcp`, `internal/mcpserver` | MCP client and server support. |
| `internal/tui` | Bubble Tea terminal UI. |
| `internal/control`, `internal/bridge` | Local API, remote control, and editor bridge surfaces. |

Keep generated local state, machine-specific paths, cache locations, and
tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
