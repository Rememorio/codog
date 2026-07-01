# Codog

Codog is a Go-native coding-agent CLI for working inside local software
repositories. It provides a Claude Code-style workflow as an ordinary single
binary: stream model responses, let the agent inspect and edit a checkout
through permission-gated tools, and keep session state in local JSONL files.

Codog is independent software. It is not affiliated with Anthropic, and it does
not attempt to recreate private Claude Code internals. The compatibility goal is
practical: familiar command shapes, local file conventions, tool contracts, and
workflows that are useful for migration and parity testing.

## Current State

Codog is pre-1.0. The local coding loop is usable, but the command surface,
configuration schema, extension APIs, and compatibility behavior can still
change.

Use it today as an experimental developer tool, not as a stable drop-in
replacement. For the exact surface supported by a build, ask the binary itself:

```sh
codog capabilities --json
```

Ready to try:

- One-shot prompts, REPL, Bubble Tea TUI, and Anthropic-compatible streaming.
- Core workspace tools, permission modes, JSONL sessions, resume, config, and
  diagnostics.

Still evolving:

- Full slash-command parity, MCP depth, skills and plugin workflows.
- IDE bridges, remote sessions, multi-agent orchestration, enterprise policy,
  sandbox portability, and updater flows.

## Why Codog

Most coding-agent CLIs hide important behavior behind hosted state, generated
launchers, editor glue, or undocumented local conventions. Codog keeps the local
runtime inspectable and testable.

- **Single binary:** build, install, and run the main agent like a normal Go
  CLI.
- **Local state:** prompts, responses, tool calls, approvals, summaries, usage,
  and exports are files you can inspect.
- **Explicit permissions:** shell and file tools run through named modes,
  trusted roots, allow and deny rules, and confirmation gates.
- **Composable extensions:** slash commands, templates, hooks, skills, plugins,
  and MCP surfaces are file-backed or protocol-backed contracts.
- **Practical compatibility:** Claude-style names and locations are supported
  where they help real workflows, without depending on private implementation
  details.

## Quick Start

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

Configure credentials with an environment variable:

```sh
export ANTHROPIC_API_KEY=<key>
```

or store a redacted local setting:

```sh
codog api-key set <key>
```

Run Codog from the repository you want it to inspect:

```sh
codog -p "summarize this repository"
```

For iterative work, use an interactive surface:

```sh
codog repl
codog tui
```

If something does not look right, start with local diagnostics:

```sh
codog setup status
codog doctor
```

## Working Model

Codog is organized around a local edit-and-review loop.

1. Start in a repository.
2. Codog loads project guidance, config, focused files, and session history.
3. The model streams a response and may request local tools.
4. Tool calls are checked against the active permission mode and rules.
5. Session records, usage, summaries, approvals, and exports stay on disk.

Common entry points:

| Goal | Entry point |
| --- | --- |
| Ask a bounded question or request a small change | `codog -p "..."` |
| Continue a multi-turn task | `codog repl`, `codog tui`, `codog --resume latest` |
| Inspect local context before a request | `codog status`, `codog context` |
| Review existing work | `codog diff`, `codog review`, `codog summary` |
| Audit or export a session | `codog history`, `codog usage`, `codog export` |

Inside REPL and TUI sessions, slash commands expose the same local surfaces in a
compact form. Use `/help` interactively and `codog capabilities --json` for
machine-readable metadata.

## Capabilities

Codog's core runtime includes:

- Provider-backed one-shot prompts, REPL, and Bubble Tea TUI.
- Anthropic Messages streaming, plus OpenAI-compatible or custom
  Anthropic-compatible endpoints when configured.
- Workspace tools for shell, read, write, edit, grep, glob, file listing, git
  inspection, todos, and background task supervision.
- Permission modes for read-only work, workspace writes, prompt-gated actions,
  allow-listed tools, and unrestricted local operation.
- JSONL sessions with resume, fork, rename, delete, export, rewind, summaries,
  compaction metadata, token usage, and estimated cost.
- Project slash commands, prompt templates, hooks, skills, plugins, MCP
  client/server surfaces, and local resources.
- Local diagnostics for config, auth, git, hooks, MCP, sandbox status, provider
  traces, and runtime health.

Some compatibility commands are intentionally thin while Codog is pre-1.0. Treat
the capability report and package tests as the source of truth for exact
behavior.

## Configuration

Configuration is layered from defaults, environment variables, user config,
project config, local overrides, and command-line flags. Shared team behavior
belongs in committed project files; personal settings belong in local overrides
or user config.

| Location | Purpose |
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

## Safety

Codog treats tool execution as reviewable local work, not as an invisible side
effect of a model response.

- File tools are scoped to trusted workspace roots.
- Shell tools can require confirmation and write structured audit records.
- Allow and deny rules can narrow the tool surface for a run.
- Hooks and policy checks can add project-specific gates.
- Sessions capture prompts, responses, tool calls, approvals, summaries, usage,
  and estimated cost.

For risky work, start with `--permission-mode read-only` or
`--permission-mode prompt`.

## Roadmap

Codog is being built in layers.

**MVP foundation:** single-binary local agent, one-shot prompt, REPL, streaming,
core workspace tools, permissions, JSONL sessions, resume, and base config.

**Practical local agent:** TUI, slash commands, MCP client, skills, hooks, cost
and token tracking, compaction, and mock parity tests.

**Claude Code-class compatibility:** IDE bridge, remote sessions, multi-agent
workflows, background jobs, notebook and LSP support, OAuth, enterprise policy,
plugin distribution, cross-platform sandboxing, and updater flows.

The final stage is a long-running compatibility program, not a short milestone.

## Development

The CLI entry point is `cmd/codog`; most behavior lives in focused packages
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

Before submitting changes:

```sh
go test ./...
go build ./cmd/codog
```

Keep generated state, secrets, local cache paths, and tool-generated attribution
out of commits.

## License

Codog is released under the [MIT License](LICENSE).
