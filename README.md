# Codog

Codog is a Go-native coding agent CLI for working inside a repository. It
provides a Claude Code-style loop: ask a question, inspect files, make edits,
run tools, and resume the session later from local state.

The project is independent and is not affiliated with Anthropic. Compatibility
with Claude Code conventions is a design goal, not a claim of API or product
equivalence.

Codog is currently pre-1.0. The local repository workflow is usable, but command
names, config fields, compatibility details, and advanced integrations may
change while the runtime is being hardened.

## Why Codog

Most coding agent CLIs concentrate a lot of behavior in a large runtime. Codog
explores a different shape: a single Go binary, small internal packages, plain
files for local state, and explicit permission checks before workspace tools are
allowed to act.

It is built for developers who want:

- a terminal-first agent that can run without a background service;
- sessions that are saved as JSONL and can be resumed, exported, or inspected;
- repository-local instructions, commands, skills, hooks, and MCP settings;
- permission modes for file access, edits, shell commands, and networked tools;
- a codebase that is practical to read, test, and extend in Go.

It is not yet a polished replacement for every hosted, IDE, enterprise, or team
workflow. Those surfaces exist in the codebase, but the strongest path today is
the local single-repository loop.

## Quick Start

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
codog -p "summarize this repository"
```

For an interactive session:

```bash
codog repl
```

Run diagnostics when something does not look right:

```bash
codog doctor
```

From a source checkout, build the local binary with:

```bash
go build ./cmd/codog
```

## How It Works

Codog starts from the current working directory and treats it as the active
workspace. Before each model turn it gathers the configured system prompt,
project instructions, focused files, session context, and tool metadata. Tool
calls then pass through workspace scope checks and permission policy before
they read, write, edit, search, or execute anything.

Sessions are stored on disk as JSONL. That keeps the agent loop inspectable:
you can list sessions, resume a previous conversation, summarize or compact a
long run, rewind recent messages, export a transcript, or examine usage and
cost estimates without depending on opaque hosted state.

## Core Surfaces

Codog keeps the first-class path small, while still exposing machine-readable
surfaces for experiments and integrations.

| Surface | Purpose |
| --- | --- |
| One-shot prompt | Run a single request from the shell with `codog -p` or `codog prompt`. |
| REPL | Keep a conversation open in the terminal with saved session state. |
| TUI | Use a Bubble Tea prompt surface with slash command completion. |
| Tools | Read, write, edit, grep, glob, list files, run shell commands, inspect git, manage todos, and call MCP tools. |
| Sessions | Resume, fork, rename, rewind, compact, export, share, and inspect saved conversations. |
| Extensions | Load project commands, prompt templates, skills, hooks, plugins, and MCP servers from local configuration. |
| Automation | Run background tasks, cron-style jobs, team workers, bridge APIs, and updater experiments. |

The broad compatibility surface is intentionally implemented as Go packages
rather than one monolithic command path. That makes individual pieces easier to
test or replace as behavior converges.

## Project Context

Codog can use repository-local files to understand how it should work on a
project. The common entry points are:

- `AGENTS.md` for agent instructions;
- `.codog.json` for shared project configuration;
- `.codog.local.json` for uncommitted local overrides;
- `.codog/commands` for Markdown command definitions;
- `.codog/templates` for reusable prompt templates;
- `.codog/skills` for project skills;
- `.codog/hooks` and `.codog/plugins` for local automation and extension
  metadata.

Compatible Claude-style instruction, command, and skill locations are also
recognized where the Go implementation supports them.

## Configuration

Configuration is layered from broad to narrow:

1. user config in the Codog config home;
2. project config in `.codog.json`;
3. local overrides in `.codog.local.json`;
4. environment variables and CLI flags.

A small project config is usually enough:

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

Credentials can come from environment variables, config files, OAuth profiles,
or `codog api-key`.

## Development

The normal validation loop is:

```bash
go test ./...
go build ./cmd/codog
```

The repository is organized around small internal packages:

- `cmd/codog` wires the CLI entry point;
- `internal/agent` owns command dispatch and top-level workflows;
- `internal/runloop` drives model turns and tool execution;
- `internal/anthropic` contains the Anthropic-compatible client and protocol
  types;
- `internal/tools` defines workspace tools, permissions, aliases, and MCP tool
  integration;
- `internal/session` stores, resumes, exports, rewinds, and compacts sessions;
- `internal/config` loads and validates layered configuration;
- `internal/tui`, `internal/mcp`, `internal/hooks`, `internal/plugins`, and
  `internal/bridge` hold optional integration surfaces.

Keep generated local state, machine-specific paths, cache locations, and
tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
