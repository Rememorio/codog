# Codog

Codog is a Go-native coding agent for working inside repositories from the
terminal. It is built as a single binary with local session state, permissioned
workspace tools, streaming model responses, and extension points for commands,
MCP, hooks, skills, plugins, and editor bridges.

Codog follows Claude Code-style workflows where they are useful, but it is an
independent implementation. It is not affiliated with Anthropic and it is not a
drop-in replacement for Claude Code.

## Status

Codog is pre-1.0 software. The core local workflow is usable, while command
names, configuration fields, compatibility behavior, and extension APIs may
still change.

Use `codog capabilities --json` when you need the exact command, tool,
protocol, and feature inventory for a build. The README describes the shape of
the project; the CLI reports the source of truth.

## Why Codog

Codog is for developers who want a coding-agent runtime that can be inspected
and changed like ordinary Go software.

- **One binary:** the main user surface is a Go CLI, not a collection of helper
  services.
- **Local-first state:** sessions, approvals, memory, config, and runtime
  artifacts live in files you can inspect directly.
- **Explicit execution boundaries:** tool permissions, workspace roots, shell
  execution, policy checks, and sandbox reporting are part of the runtime
  contract.
- **Compatibility where useful:** Claude-style tool names, slash commands,
  project files, and protocols are mirrored when they help migration or testing.
- **Extension-ready:** MCP, hooks, skills, plugins, prompt templates, local
  resources, and worker APIs are treated as first-class surfaces.

## Quick Start

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

Configure an Anthropic-compatible key through `ANTHROPIC_API_KEY` or
`codog api-key set`, then run Codog from the repository you want it to inspect:

```bash
codog -p "summarize this repository"
```

For multi-turn work:

```bash
codog repl
codog tui
```

Useful local inspection commands:

```bash
codog doctor
codog context
codog capabilities --json
```

## Common Workflows

| Goal | Entry point |
| --- | --- |
| Ask one question about a repo | `codog -p "..."` |
| Work interactively in the terminal | `codog repl` |
| Use the Bubble Tea interface | `codog tui` |
| Continue saved work | `codog --resume latest repl` |
| Inspect prompt context before a request | `codog context` |
| Check local auth, config, hooks, MCP, git, and sandbox state | `codog doctor` |
| See the exact feature inventory | `codog capabilities --json` |

## What Codog Provides

Codog is organized around a few runtime surfaces instead of a large plugin-like
core.

**Model runtime.** Codog supports one-shot prompts, REPL and TUI sessions,
streaming Anthropic Messages responses, OpenAI-compatible chat streaming,
JSONL-backed session resume, prompt history, summaries, compaction, token
usage, and estimated cost tracking.

**Workspace tools.** The agent can read, write, edit, search, glob, list files,
run shell commands, manage background tasks, inspect git state, call notebooks
and LSP helpers, and expose Claude-compatible tool aliases where implemented.

**Local control plane.** The CLI includes configuration inspection, permission
and allow-list controls, project memory, focus paths, todos, diagnostics,
doctor checks, sandbox status, remote-control HTTP endpoints, ACP/Zed bridge
support, and editor handoff state.

**Extension surfaces.** Codog can load Markdown slash commands, prompt
templates, hooks, skills, plugins, MCP servers and clients, plugin marketplace
metadata, worker definitions, and local MCP resources.

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

## Safety Model

Codog treats tool execution as runtime behavior, not just as prompt text.

- File tools are scoped to trusted workspace roots.
- Shell tools can require confirmation, record structured results, and report
  sandbox request and runtime status.
- Hooks and policy checks can add project-specific gates.
- Sessions record prompts, model responses, tool calls, approvals, token usage,
  cost data, summaries, and compaction metadata for later review or resume.

## Development

The CLI entry point is `cmd/codog`. Most behavior lives in focused packages
under `internal/`.

```bash
go test ./...
go build ./cmd/codog
```

Keep generated state, local cache paths, secrets, and tool attribution out of
code, docs, commit messages, and examples.

## License

Codog is released under the [MIT License](LICENSE).
