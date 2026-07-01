# Codog

Codog is a Go-native coding agent for working inside software repositories from
the terminal.

It aims to make the core Claude Code-style workflow inspectable, portable, and
easy to modify as ordinary Go software: one binary, local session state,
explicit tool permissions, streaming model responses, and extension points that
can be tested without a hidden desktop runtime.

Codog is an independent project. It is not affiliated with Anthropic, and it is
not a drop-in replacement for Claude Code.

## Status

Codog is pre-1.0 software. The local agent workflow is usable, but command
names, configuration fields, compatibility shims, and extension APIs may still
change.

The CLI is the source of truth for a given build:

```bash
codog capabilities --json
```

Use that output when you need an exact inventory of commands, tools, protocols,
and compatibility features.

## Why It Exists

Coding-agent tools often span hosted APIs, local CLIs, editor integrations, and
extension systems. Codog keeps the local runtime small and explicit:

- A single Go binary is the main runtime surface.
- Sessions, approvals, summaries, memory, and runtime artifacts are stored as
  local files.
- Tool execution is explicit: workspace roots, permission modes, shell access,
  allow lists, sandbox reporting, and policy checks are part of the runtime
  contract.
- Claude-style project files, tool names, slash commands, and protocol shapes
  are mirrored where they help migration or parity testing.
- MCP, hooks, skills, plugins, prompt templates, and editor bridges are treated
  as first-class extension surfaces rather than afterthoughts.

The goal is not to clone every private implementation detail of another tool.
The goal is a practical local agent runtime that can be read, modified, tested,
and shipped by Go developers.

## Install

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

For development from a checkout:

```bash
go build ./cmd/codog
```

## First Run

Configure an Anthropic-compatible key with either `ANTHROPIC_API_KEY` or the
local key store (`codog api-key set KEY`).

Then run Codog from the repository you want it to inspect:

```bash
codog -p "summarize this repository"
```

For an interactive session, use the REPL or the Bubble Tea TUI:

```bash
codog repl
codog tui
```

## Core Workflow

Codog is organized around a few durable surfaces.

**Ask and iterate.** Use one-shot prompts for quick repository questions, then
move into REPL or TUI sessions for multi-turn work. Sessions are stored as JSONL
so they can be resumed, inspected, exported, summarized, compacted, and used for
cost or token accounting.

**Let the agent work in a repository.** The built-in tools cover the common
coding loop: read files, write files, apply edits, glob, grep, run shell
commands, inspect git state, track todos, and manage background tasks. Tool
aliases are kept close to Claude-style names where compatibility is useful.

**Extend the runtime locally.** Projects can define slash commands, prompt
templates, hooks, skills, plugins, MCP servers and clients, worker definitions,
and local resources. The extension formats are file-based so they can be
versioned with the project when that makes sense.

**Keep execution visible.** Permission modes, allowed and disallowed tools,
workspace roots, sandbox status, hooks, approvals, audit records, and policy
checks are part of the runtime rather than informal prompt instructions.

## Configuration

Configuration is layered from defaults, environment variables, user config,
project config, local overrides, and command-line flags. Repository-local files
are intentionally plain:

| Path | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions for Codog. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Uncommitted local overrides. |
| `.codog/commands` | Markdown slash commands. |
| `.codog/templates` | Prompt templates. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Local automation hooks. |
| `.codog/plugins` | Plugin manifests and packaged extension content. |

Compatible `.claude` instruction, command, and skill locations are loaded where
that compatibility is useful and implemented.

Useful inspection commands:

```bash
codog doctor
codog config paths
codog context
```

## Safety Model

Codog treats tool execution as runtime behavior, not just model output.

- File tools are scoped to trusted workspace roots.
- Shell tools can require confirmation and record structured results.
- Permission modes can make a session read-only, workspace-write, prompt-gated,
  or unrestricted.
- Hooks and policy checks can add project-specific gates before or after tool
  execution.
- Sessions record prompts, model responses, tool calls, approvals, summaries,
  compaction metadata, token usage, and estimated cost for later review.

## Development

The CLI entry point is `cmd/codog`. Most behavior lives in focused packages
under `internal/`.

```bash
go test ./...
go build ./cmd/codog
```

When adding user-facing behavior, keep the README high-level and put precise
runtime truth in the CLI output, tests, or focused documentation. Avoid
committing generated state, local cache paths, secrets, or tool-generated
attribution.

## License

Codog is released under the [MIT License](LICENSE).
