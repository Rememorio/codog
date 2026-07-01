# Codog

Codog is a Go-native coding agent for repository work in the terminal. It runs
as a single binary and combines streaming model responses, permissioned
workspace tools, resumable local sessions, and extension points such as slash
commands, MCP, hooks, skills, and plugins.

Codog follows Claude Code-style workflows where they are useful, but it is an
independent implementation. It is not affiliated with Anthropic and is not a
drop-in replacement for Claude Code.

## Why Codog

Codog exists for developers who want a coding-agent runtime that can be read,
debugged, and changed like ordinary Go software.

- **Local-first state.** Sessions, config, approvals, memory, and runtime
  artifacts are stored as files that can be inspected or deleted directly.
- **One binary.** The main user surface is a Go CLI instead of a stack of helper
  services.
- **Explicit execution boundaries.** Workspace tools, shell commands,
  permission modes, policy checks, and sandbox reporting are part of the runtime
  contract.
- **Practical compatibility.** Claude-style command names, tool aliases,
  project files, and protocols are mirrored when they make migration or
  experimentation easier.
- **Extensible by default.** Slash commands, hooks, skills, MCP, plugins, IDE
  bridge surfaces, and worker APIs are first-class parts of the design.

## Status

Codog is pre-1.0 software under active development. The local coding workflow is
usable, but command names, config fields, compatibility behavior, and extension
APIs can still change.

Use it for experimentation, development, and studying how a coding-agent runtime
can be assembled in Go. Do not treat it yet as a mature enterprise agent
platform or a complete Claude Code clone.

For the exact capabilities of a build, use the machine-readable inventory:

```bash
codog capabilities --json
```

## Quick Start

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
```

Run it from the repository you want it to inspect:

```bash
codog -p "summarize this repository"
```

For multi-turn work, start an interactive shell or the terminal UI:

```bash
codog repl
codog tui
```

## Core Capabilities

| Area | What Codog provides |
| --- | --- |
| Conversation | One-shot prompts, REPL, Bubble Tea TUI, text/JSON/streaming output, resumable JSONL sessions. |
| Providers | Anthropic Messages streaming and OpenAI-compatible chat streaming. |
| Workspace tools | Bash, read, write, edit, multi-edit, grep, glob, ls, git helpers, background tasks, and Claude-style aliases. |
| Safety | Workspace boundaries, permission confirmation, approval tokens, policy evaluation, sandbox configuration, and sandbox runtime diagnostics. |
| Context | Project instructions, memory, focused files, prompt history, session summaries, auto-compaction, token and cost tracking. |
| Extensions | Slash commands, MCP client/server, hooks, skills, plugins, local resources, prompt templates, and marketplace metadata. |
| Integrations | IDE/editor bridge, remote-control HTTP API, ACP/Zed bridge, notebooks, LSP helpers, multi-agent workers, and updater surfaces. |

The list above is a readable overview, not the source of truth. The CLI exposes
the authoritative command, tool, protocol, and feature inventory through
`codog capabilities --json`.

## Configuration

Codog loads configuration from global defaults, environment variables, project
files, and local overrides. Repository-local files are intentionally simple:

| Path | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions for the agent. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Uncommitted local overrides. |
| `.codog/commands` | Markdown slash commands. |
| `.codog/templates` | Reusable prompt templates. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Local automation hooks. |
| `.codog/plugins` | Plugin manifests and packaged extension content. |

Compatible `.claude` instruction, command, and skill locations are loaded where
that compatibility is useful and implemented.

## Safety Model

Codog treats tool execution as runtime behavior, not only as prompt text.

- File tools are scoped to trusted workspace roots.
- Shell tools can require confirmation, record structured results, and expose
  sandbox request and runtime status.
- Hooks and policy checks can add project or team-specific gates.
- Sessions record prompts, model responses, tool calls, approvals, token usage,
  cost data, summaries, and compaction metadata for later review or resume.

## Development

The main entry point is `cmd/codog`; most behavior lives in focused packages
under `internal/`.

```bash
go test ./...
go build ./cmd/codog
```

Keep generated state, local cache paths, secrets, and tool attribution out of
code, docs, commit messages, and examples.

## License

Codog is released under the [MIT License](LICENSE).
