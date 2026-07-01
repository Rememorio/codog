# Codog

Codog is a Go-native coding agent CLI for software repositories.

It brings a Claude Code-style workflow into a single inspectable binary: ask a
model to work in a checkout, stream its response, let it use local tools under a
permission model, and keep the resulting session in local JSONL state.

Codog is independent, is not affiliated with Anthropic, and does not try to
recreate private implementation details. The goal is behavioral compatibility
where it is useful, with a runtime that remains small enough to read, test, and
operate as an ordinary Go program.

## Status

Codog is pre-1.0 software. The local agent workflow is usable, but command
names, configuration fields, extension APIs, and compatibility shims can still
change.

Use it today as an experimental developer tool, not as a stable drop-in
replacement for Claude Code. For the exact surface supported by a build, ask the
binary:

```sh
codog capabilities --json
```

## Why Codog

Most coding-agent CLIs hide important behavior across a hosted service, a local
launcher, editor integrations, generated state, and tool-specific conventions.
Codog keeps the local side explicit.

- **One binary.** The main runtime is a Go executable that can be built,
  installed, tested, and shipped like a normal CLI.
- **Local state.** Sessions, summaries, usage data, approvals, memory, and
  generated artifacts live in project-owned or user-owned files.
- **Visible execution.** Tool calls go through named permissions, workspace
  roots, audit records, and confirmation gates.
- **Composable extensions.** Slash commands, templates, hooks, skills, plugins,
  MCP, and bridge surfaces are file-backed or protocol-backed contracts.
- **Compatibility by behavior.** Claude-style names and file locations are
  mirrored where they help migration, automation, or parity testing.

## Quick Start

Install Codog with Go 1.24 or newer, configure an Anthropic-compatible API key,
and run it from the repository you want it to inspect:

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
codog api-key set <key>
codog -p "summarize this repository"
```

For longer sessions, use the interactive surfaces:

```sh
codog repl
codog tui
```

## What It Does

Codog centers on the local coding loop rather than a separate web workspace.

| Area | Current surface |
| --- | --- |
| Agent loop | One-shot prompts, REPL, Bubble Tea TUI, side questions, streaming Anthropic-compatible requests, and OpenAI-compatible endpoints. |
| Workspace tools | Read, write, edit, grep, glob, list files, run shell commands, inspect git state, manage todos, and supervise background tasks. |
| Sessions | JSONL transcripts, resume, fork, rename, delete, export, rewind, summary, compaction, token usage, and cost views. |
| Extensions | Project slash commands, prompt templates, hooks, skills, plugins, MCP client/server surfaces, custom tool aliases, and local resources. |
| Integrations | IDE/ACP bridge, remote-control HTTP API, OAuth provider profiles, sandbox reporting, updater flow, and policy checks. |

This table is intentionally a map, not a manual. For automation and parity
checks, prefer `codog capabilities --json`.

## Daily Use

Use a one-shot prompt for bounded work, such as asking Codog to explain a file,
summarize a diff, or make a small change. Use the REPL or TUI when the task
needs several turns. Codog records the transcript and tool calls as the session
evolves, so the same work can be resumed, summarized, exported, compacted, or
audited later.

When the local environment is the problem, start from the diagnostic surfaces:
`doctor` for setup and config checks, `status` for runtime state, and `context`
for the prompt context Codog is preparing to send.

## Configuration

Configuration is layered from defaults, environment variables, user config,
project config, local overrides, and command-line flags. Repository-local files
are plain text so teams can commit shared behavior and keep personal overrides
uncommitted.

| Path | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions loaded into Codog's working context. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Local overrides that should usually stay uncommitted. |
| `.codog/commands` | Markdown slash commands. |
| `.codog/templates` | Prompt templates. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Hook definitions and local automation. |
| `.codog/plugins` | Plugin manifests and packaged extension content. |

Compatible `.claude` instruction, command, and skill locations are loaded where
that compatibility is implemented.

## Safety Model

Codog treats tool execution as something that should be reviewable, not as a
side effect hidden inside a model response.

- File tools are scoped to trusted workspace roots.
- Shell tools can require confirmation and write structured audit records.
- Permission modes cover read-only, workspace-write, prompt-gated, and
  unrestricted runs.
- Allow and deny rules can narrow the available tool surface.
- Hooks and policy checks can add project-specific gates.
- Sessions capture prompts, model responses, tool calls, approvals, summaries,
  compaction metadata, token usage, and estimated cost.

## Development

The CLI entry point is `cmd/codog`. Most behavior lives in focused packages
under `internal/`.

| Area | Package |
| --- | --- |
| CLI orchestration | `internal/agent` |
| Model streaming | `internal/anthropic` |
| Workspace tools | `internal/tools` |
| Sessions | `internal/session` |
| Configuration | `internal/config` |
| Hooks and skills | `internal/hooks`, `internal/skills` |
| MCP | `internal/mcp`, `internal/mcpserver` |
| TUI | `internal/tui` |

Run the test suite before submitting changes:

```sh
go test ./...
```

Keep generated state, local cache paths, secrets, and tool-generated attribution
out of commits.

## License

Codog is released under the [MIT License](LICENSE).
