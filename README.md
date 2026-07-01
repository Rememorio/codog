# Codog

Codog is a Go-native coding agent for local repositories. It provides a
Claude Code-style workflow as a normal single binary: ask a model about a
checkout, let it use reviewable local tools, and keep the session history in
plain files.

Codog is independent software. It is not affiliated with Anthropic, and it does
not try to reproduce private Claude Code internals. The compatibility target is
observable behavior: familiar commands, slash commands, tool contracts,
configuration files, session records, MCP surfaces, and local developer
workflows.

> Status: pre-1.0. The local agent loop is usable, but command details,
> configuration fields, extension APIs, and compatibility behavior may still
> change between builds.

## Highlights

- Single Go binary for one-shot prompts, REPL sessions, and a Bubble Tea TUI.
- Anthropic Messages streaming, Anthropic-compatible endpoints, and
  OpenAI-compatible chat streaming.
- Permission-gated workspace tools for shell, read, write, edit, grep, glob,
  git inspection, notebooks, and LSP/code intelligence.
- JSONL sessions with resume, fork, rename, rewind, export, summaries, usage,
  token, and cost metadata.
- Project and user configuration layers, including `.codog` and compatible
  `.claude` instruction, command, and skill locations where implemented.
- Extension surfaces for slash commands, prompt templates, hooks, skills,
  plugins, local MCP data, and MCP client/server flows.
- Local safety controls: read-only mode, workspace-write mode, prompt approval,
  allow and deny lists, trusted roots, audit records, and sandbox reporting.
- Parity tooling for mock providers, capability reports, protocol contracts,
  and repeatable compatibility tests.

## Quick Start

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY=<key>
codog -p "summarize this repository"
```

For an interactive session, run:

```sh
codog repl
```

Use `codog tui` if you prefer the full-screen terminal interface. If setup,
auth, or local permissions look wrong, start with:

```sh
codog doctor
codog setup status
```

## How Codog Fits Into Daily Work

Codog is meant to sit beside `git`, your editor, and your normal shell. The
common loop is:

1. Start in the repository you want the agent to inspect.
2. Ask a focused question or request a small change with `codog -p`.
3. Move to `codog repl` or `codog tui` when the work needs multiple turns.
4. Review proposed shell and file operations through the active permission mode.
5. Resume or inspect the session later from the JSONL record on disk.

Useful entry points:

| Need | Start here |
| --- | --- |
| Ask a bounded question | `codog -p "..."` |
| Continue a session | `codog repl`, `codog tui`, `codog --resume latest` |
| See what the agent knows | `codog status`, `codog context`, `codog files` |
| Review repository changes | `codog diff`, `codog review`, `codog security-review` |
| Manage saved sessions | `codog sessions`, `codog history`, `codog summary`, `codog export` |
| Check cost and context | `codog usage`, `codog cost`, `codog compact` |

Inside REPL and TUI sessions, `/help` shows the slash-command surface. For the
complete command, tool, protocol, and feature surface of a build, use:

```sh
codog capabilities --json
```

## Safety And Local State

Codog treats model-requested tool execution as local work that should be
reviewable and recoverable.

- Permission modes control whether tools can read, write, run commands, or ask
  before taking action.
- Allow and deny lists narrow which tools are available for a run.
- Trusted roots and broad-cwd guards reduce accidental access outside the
  intended workspace.
- Audit records capture prompts, responses, tool calls, approvals, summaries,
  usage, and estimated cost.
- Session files are JSONL so they can be inspected, exported, resumed, and used
  in tests.

For unfamiliar repositories, start with:

```sh
codog --permission-mode read-only -p "explain the project structure"
```

## Project Files

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

## Compatibility Scope

Codog is a compatibility-oriented reimplementation, not a private-internals
clone. The project focuses on behavior that can be observed, tested, and kept
stable:

- CLI and slash command shapes.
- Tool schemas and permission behavior.
- Session records, compaction metadata, usage, and cost accounting.
- MCP resources, prompts, tools, skills, hooks, and plugin manifests.
- IDE bridge, remote-control, multi-agent, background-task, notebook, LSP,
  sandbox, OAuth, policy, and updater surfaces.

Some of those surfaces are already useful; others are intentionally thin while
the contracts are being hardened. When exact support matters, trust
`codog capabilities` and the test suite over README prose.

## Development

The CLI entry point is `cmd/codog`. Most behavior lives in focused packages
under `internal/`, including `agent`, `anthropic`, `tools`, `session`, `config`,
`slash`, `hooks`, `skills`, `plugins`, `mcp`, `codeintel`, and `tui`.

Run the standard checks before submitting changes:

```sh
go test ./...
go build ./cmd/codog
```

Keep generated state, secrets, local cache paths, and tool-specific attribution
out of commits.

## License

Codog is released under the [MIT License](LICENSE).
