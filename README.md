# Codog

Codog is a Go-native coding-agent CLI for local repositories. It aims to give
developers a Claude Code-style workflow as a normal single binary: ask a model
to reason about a checkout, let it use permission-gated local tools, and keep
the session history on disk where it can be inspected, resumed, exported, and
tested.

Codog is independent software. It is not affiliated with Anthropic and does not
attempt to reproduce private Claude Code internals. The compatibility goal is
practical: familiar command shapes, file conventions, tool contracts, and local
workflows that make migration and parity testing possible.

> Status: pre-1.0. The local agent loop is usable, but command details,
> configuration fields, extension APIs, and compatibility behavior may still
> change between builds.

## Why It Exists

Most coding-agent CLIs mix local execution with hosted state, editor-specific
glue, generated launchers, or undocumented file conventions. Codog keeps the
runtime small enough to build and inspect, while still covering the surfaces a
daily coding assistant needs.

Codog is useful if you want:

- A Go implementation that builds into one portable binary.
- Local JSONL sessions instead of opaque conversation state.
- Reviewable shell and file operations with explicit permission modes.
- A compatibility lab for Claude-style commands, tools, MCP, skills, hooks, and
  project configuration.
- A codebase where the agent runtime, tests, and protocol adapters live in the
  same repository.

## What Works Today

Codog already includes the core local loop and a broad compatibility surface.
Some advanced commands are still intentionally thin, so the binary's capability
report is the source of truth for a specific build.

| Area | Current support |
| --- | --- |
| Agent entry points | One-shot prompts, REPL, Bubble Tea TUI, side-question sessions, resume, fork, rename, rewind, and export. |
| Model providers | Anthropic Messages streaming, Anthropic-compatible endpoints, OpenAI-compatible chat streaming, API key management, and OAuth profile plumbing. |
| Workspace tools | Shell, PowerShell, read, write, edit, multi-edit, grep, glob, file listing, git inspection, todos, background tasks, notebook read/edit, and LSP/code-intel helpers. |
| Safety controls | Read-only, workspace-write, prompt, allow-list, and danger modes, plus trusted roots, tool allow/deny rules, audit records, and sandbox status reporting. |
| Local state | JSONL sessions, prompt history, summaries, compaction metadata, token usage, estimated cost, exports, cache stats, and recovery ledgers. |
| Extension surfaces | Slash commands, prompt templates, hooks, skills, plugins, local MCP client/server resources, local prompts, and marketplace scaffolding. |
| Integrations | Git workflows, GitHub PR helpers, ACP/Zed bridge metadata, IDE bridge state, remote-control API, notifications, voice command hooks, and updater scaffolding. |

Run this when you need the exact command, tool, protocol, and feature surface:

```sh
codog capabilities --json
```

## Quick Start

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY=<key>
codog -p "summarize this repository"
```

For an interactive session, run `codog repl` or `codog tui` from the repository
you want Codog to inspect. If setup or auth looks wrong, start with
`codog doctor` and `codog setup status`.

## Daily Workflow

| Need | Entry point |
| --- | --- |
| Ask a bounded question or request a focused edit | `codog -p "..."` |
| Keep working across turns | `codog repl`, `codog tui`, `codog --resume latest` |
| Inspect what Codog will see | `codog status`, `codog context`, `codog files` |
| Review local changes | `codog diff`, `codog review`, `codog security-review` |
| Manage session records | `codog sessions`, `codog history`, `codog summary`, `codog export` |
| Check cost and context size | `codog usage`, `codog cost`, `codog compact` |

Inside REPL and TUI sessions, use `/help` for the slash-command surface. Most
commands also support `--json` or `--output-format json` for scripts and parity
tests.

## Local Files

Codog layers configuration from defaults, environment variables, user config,
project config, local overrides, and command-line flags. Shared team behavior
belongs in committed project files; personal preferences and secrets belong in
local overrides or environment variables.

| Path | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions loaded into the working context. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Uncommitted local overrides. |
| `.codog/commands` | Markdown slash commands. |
| `.codog/templates` | Prompt templates. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Hook definitions and local automation. |
| `.codog/plugins` | Plugin manifests and packaged extension content. |

Compatible `.claude` instruction, command, and skill locations are loaded where
that compatibility has been implemented.

## Safety Model

Codog treats model-requested tool execution as local work that should be
auditable.

- File and shell tools are checked against the active permission mode.
- Trusted roots and broad-cwd guards limit accidental access outside the
  workspace.
- Allow and deny rules can narrow which tools are available for a run.
- Prompt-gated modes ask before risky actions.
- Sessions capture prompts, responses, tool calls, approvals, summaries, usage,
  and estimated cost.

For unfamiliar repositories, start with `--permission-mode read-only` or
`--permission-mode prompt`.

## Compatibility Scope

Codog is a compatibility-oriented reimplementation, not a private-internals
clone. The project intentionally focuses on observable behavior: command names,
slash commands, tool schemas, local files, session records, MCP surfaces, and
developer workflows.

That also means compatibility is measured in tests, mock provider traces, and
local protocol contracts. If behavior matters for another tool, it should be
captured in a repeatable test rather than only described in prose.

## Roadmap

| Stage | Target | Status |
| --- | --- | --- |
| MVP foundation | Single binary, one-shot prompt, REPL, streaming, core tools, permissions, JSONL sessions, resume, and base config. | Implemented and still being hardened. |
| Practical local agent | TUI, slash commands, MCP client, skills, hooks, cost and token tracking, compaction, and mock parity harness. | Broad support exists; depth and compatibility are still expanding. |
| Claude Code-class parity | IDE bridge, remote sessions, multi-agent workflows, background jobs, notebook and LSP support, OAuth, enterprise policy, plugin distribution, cross-platform sandboxing, and updater flows. | Experimental surfaces exist; full parity is a long-running program. |

## Development

The CLI entry point is `cmd/codog`. Most behavior lives in focused packages
under `internal/`, including `agent`, `anthropic`, `tools`, `session`, `config`,
`slash`, `hooks`, `skills`, `plugins`, `mcp`, and `tui`.

Use the standard Go checks before submitting changes:

```sh
go test ./...
go build ./cmd/codog
```

Keep generated state, secrets, local cache paths, and tool-specific attribution
out of commits.

## License

Codog is released under the [MIT License](LICENSE).
