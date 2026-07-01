# Codog

Codog is an experimental Go-native coding agent for terminal-based repository
work. It is built around a small local runtime: one binary, streamed model
calls, permissioned workspace tools, JSONL sessions, resumable state, and
plain-file configuration.

The project is inspired by Claude Code-style workflows, but it is an
independent implementation. It is not affiliated with Anthropic and is not a
drop-in replacement for Claude Code.

## Why Codog Exists

Codog is for people who want an inspectable coding-agent runtime that can be
built, debugged, and extended as ordinary Go code.

| Goal | What that means in practice |
| --- | --- |
| Local-first state | Sessions, memory, config, hooks, and extension metadata live in files that can be reviewed and versioned. |
| One deployable binary | The CLI, REPL, TUI, tools, MCP surfaces, bridges, and local workflow helpers ship from one Go program. |
| Permissioned automation | Shell, file, git, browser, notebook, LSP, worker, and policy tools pass through explicit workspace and permission checks. |
| Compatibility where useful | Claude-style command names, aliases, project files, skills, hooks, and protocols are mirrored when they improve daily use. |

## Current Status

Codog is pre-1.0 and moving quickly. The local agent workflow is usable, but
APIs, command names, configuration fields, and compatibility behavior may still
change.

Today, Codog is most useful for:

- one-shot prompts and interactive repository sessions;
- streamed Anthropic-compatible and OpenAI-compatible model requests;
- permissioned workspace tools for file edits, shell commands, search, git,
  notebooks, web access, LSP-style code intelligence, and background tasks;
- resumable JSONL sessions with summaries, history, export, cost and token
  tracking, and compaction;
- project instructions, memory, focused files, slash commands, skills, hooks,
  MCP client/server support, plugins, and local policy surfaces.

It is not yet something to treat as a mature enterprise agent platform or a
complete Claude Code clone. The repository is intentionally explicit about that
boundary so the implementation can stay testable and incremental.

## Quick Start

Codog requires Go 1.24 or newer and an Anthropic-compatible API key.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
```

Run it from the repository you want Codog to inspect:

```bash
codog -p "summarize this repository"
codog repl
```

For a local checkout:

```bash
go test ./...
go build ./cmd/codog
```

## Everyday Workflow

Start with a single prompt when you want a focused answer:

```bash
codog -p "find the riskiest code paths in this change"
```

Switch to the REPL or TUI when the work needs multiple turns:

```bash
codog repl
codog tui
```

Use `codog --help` for the human CLI surface and
`codog capabilities --json` for the machine-readable command, tool, protocol,
and feature inventory. The README intentionally does not duplicate the full
command reference.

## How It Works

Each turn follows the same local pipeline:

1. load project instructions, layered config, memory, focused files, session
   history, slash commands, skills, hooks, and available tools;
2. assemble a provider request and stream the model response;
3. route tool calls through workspace scope, permission mode, allow and deny
   rules, policy checks, and hooks;
4. append conversation events and tool results to a local JSONL session that can
   be resumed, exported, summarized, compacted, or inspected.

This design favors boring, inspectable state over hidden services. When
something goes wrong, the relevant files should be easy to find and diff.

## Project Files

Codog reads these repository-local files when present:

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

Where implemented, compatible `.claude` instruction, command, and skill
locations are also loaded.

## Architecture

The CLI entry point is intentionally thin. Most behavior lives in focused
internal packages:

- `internal/agent` coordinates turns, sessions, context, commands, and output.
- `internal/anthropic` and related provider packages handle streaming model
  protocols.
- `internal/tools` contains the permissioned tool registry and Claude-style
  aliases.
- `internal/config`, `internal/session`, `internal/hooks`, `internal/skills`,
  `internal/mcp`, `internal/plugins`, and `internal/policyengine` own their
  respective runtime surfaces.
- `internal/tui`, `internal/bridge`, `internal/workers`, `internal/sandbox`,
  and `internal/updater` provide the larger compatibility and automation
  layers.

## Roadmap

Codog is being built in three broad phases:

| Phase | Focus |
| --- | --- |
| MVP | One binary, one-shot prompt, REPL, streaming model calls, workspace tools, permission confirmation, JSONL sessions, resume, and basic config. |
| Practical daily use | Bubble Tea TUI, slash commands, MCP client, skills, hooks, cost and token tracking, auto-compaction, and mock parity harnesses. |
| Claude Code-class parity | IDE bridge, remote sessions, multi-agent work, background jobs, notebooks, LSP surfaces, OAuth, policy controls, plugin marketplace, sandboxing, and updater support. |

The third phase is intentionally large. It should be treated as a long-running
compatibility program, not a short single-person milestone.

## Development

Run the test suite before sending changes:

```bash
go test ./...
```

Keep generated state, machine-specific paths, local cache locations, secrets,
and tool attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
