# Codog

Codog is a Go-native coding agent CLI for working inside software repositories.

It aims to provide a Claude Code-style local workflow as an ordinary Go binary:
stream model responses, let the agent inspect and edit the checkout through
scoped tools, require confirmation for sensitive operations, and keep session
state in local JSONL files.

Codog is independent software. It is not affiliated with Anthropic, and it does
not attempt to recreate private Claude Code internals. The compatibility target
is practical behavior: command names, local file conventions, tool contracts,
and workflows that make migration and parity testing useful.

## Status

Codog is pre-1.0. The local coding loop is usable, but command names,
configuration fields, extension APIs, and compatibility surfaces can still
change.

Use it today as an experimental developer tool, not as a stable drop-in
replacement. The supported surface of a build is intentionally queryable:

```sh
codog capabilities --json
```

## Why Codog Exists

Most coding-agent CLIs split important behavior across hosted services, local
launchers, editor integrations, generated state, and hidden tool conventions.
Codog keeps the local runtime inspectable.

- **Single binary:** build, test, install, and ship the main runtime like any
  other Go CLI.
- **Local state:** prompts, responses, tool calls, approvals, summaries, cost
  data, and exports are stored in files you can inspect.
- **Explicit permissions:** file and shell tools run through named permission
  modes, trusted roots, and confirmation gates.
- **Composable extensions:** slash commands, templates, hooks, skills, plugins,
  MCP, and bridge APIs are file-backed or protocol-backed contracts.
- **Behavioral compatibility:** Claude-style names and locations are supported
  where they help real workflows, without coupling Codog to private details.

## Install

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

Then configure an Anthropic-compatible key by either storing it locally:

```sh
codog api-key set <key>
```

or using the environment:

```sh
export ANTHROPIC_API_KEY=<key>
```

OpenAI-compatible endpoints can be selected with provider settings or the
`--base-url` flag when the target API follows a compatible streaming contract.

## First Run

Run Codog from the repository you want it to inspect:

```sh
codog -p "summarize this repository"
```

For longer work, start an interactive surface:

```sh
codog repl
codog tui
```

Useful first diagnostics:

```sh
codog doctor
codog status
codog sessions list
```

## Core Workflows

Codog is organized around a local edit-and-review loop rather than a remote web
workspace.

| Workflow | What to use |
| --- | --- |
| Ask a bounded question or request a small change | `codog -p "..."` |
| Continue a multi-turn task | `codog repl`, `codog tui`, `codog --resume latest` |
| Inspect what the agent will see | `codog context`, `codog files`, `codog search` |
| Review or summarize existing work | `codog diff`, `codog review`, `codog summary` |
| Audit or export a session | `codog history`, `codog usage`, `codog export` |
| Inspect the complete command and tool surface | `codog capabilities --json` |

Inside a session, slash commands expose the same local surfaces in a more compact
form. Use `/help` for the interactive list and `/capabilities` for
machine-readable metadata.

## What Works Today

The current implementation includes the pieces needed for an experimental local
agent runtime:

- One-shot prompts, REPL, and Bubble Tea TUI.
- Anthropic-compatible streaming and OpenAI-compatible endpoint support.
- Workspace tools for shell, read, write, edit, grep, glob, file listing, git
  inspection, todos, and background task supervision.
- Permission modes for read-only, workspace-write, prompt-gated, allow-listed,
  and unrestricted operation.
- JSONL sessions with resume, fork, rename, delete, export, rewind, summaries,
  compaction metadata, token usage, and estimated cost.
- Project slash commands, templates, hooks, skills, plugins, MCP client/server
  surfaces, local resources, and custom tool aliases.
- Bridge and automation surfaces for IDE/ACP, remote control, OAuth profiles,
  policy checks, sandbox status, and updater flows.

Some compatibility surfaces are intentionally thin while the project is pre-1.0.
Prefer `codog capabilities --json` and package tests over README prose when you
need exact behavior.

## Configuration

Configuration is layered from defaults, environment variables, user config,
project config, local overrides, and command-line flags. Shared team behavior
should live in committed project files; personal settings should stay in local
overrides or the user config.

| Path | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions loaded into Codog's working context. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Local overrides that normally stay uncommitted. |
| `.codog/commands` | Markdown slash commands. |
| `.codog/templates` | Prompt templates. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Hook definitions and local automation. |
| `.codog/plugins` | Plugin manifests and packaged extension content. |

Compatible `.claude` instruction, command, and skill locations are loaded where
that compatibility has been implemented.

## Safety Model

Codog treats tool execution as reviewable local work, not as an invisible side
effect of a model response.

- File tools are scoped to trusted workspace roots.
- Shell tools can require confirmation and write structured audit records.
- Allow and deny rules can narrow the tool surface for a run.
- Hooks and policy checks can add project-specific gates.
- Sessions capture prompts, responses, tool calls, approvals, summaries,
  compaction metadata, token usage, and estimated cost.

For risky work, start with `--permission-mode read-only` or
`--permission-mode prompt`.

## Project Layout

The CLI entry point is `cmd/codog`. Most behavior is split into focused packages
under `internal/`.

| Area | Package |
| --- | --- |
| CLI orchestration | `internal/agent` |
| Model streaming | `internal/anthropic` |
| Workspace tools | `internal/tools` |
| Sessions | `internal/session` |
| Configuration | `internal/config` |
| Slash commands | `internal/slash` |
| Hooks, skills, and plugins | `internal/hooks`, `internal/skills`, `internal/plugins` |
| MCP | `internal/mcp`, `internal/mcpserver` |
| TUI | `internal/tui` |

## Roadmap

Codog is being built in layers.

| Stage | Scope |
| --- | --- |
| MVP | Single-binary local agent, one-shot prompt, REPL, streaming, core workspace tools, permissions, JSONL sessions, resume, and base config. |
| Practical local agent | TUI, slash commands, MCP client, skills, hooks, cost and token tracking, compaction, and mock parity tests. |
| Claude Code-class compatibility | IDE bridge, remote sessions, multi-agent workflows, background jobs, notebook and LSP support, OAuth, enterprise policy, plugin distribution, cross-platform sandboxing, and updater flows. |

The last stage is a long-running compatibility program rather than a short
single-person milestone.

## Development

Run the test suite before submitting changes:

```sh
go test ./...
```

Keep generated state, local cache paths, secrets, and tool-generated attribution
out of commits. When changing behavior, update the relevant package tests and
prefer machine-readable capability output for broad surface assertions.

## License

Codog is released under the [MIT License](LICENSE).
