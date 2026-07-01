# Codog

Codog is a Go-native coding-agent CLI for working in a repository from the
terminal. It aims for a Claude Code-like workflow while staying local-first,
inspectable, and easy to ship as a single binary.

Codog is independent software and is not affiliated with Anthropic. Claude Code
compatibility is a product goal, not a claim that every command or edge case is
identical today.

## Why Codog

- **One binary**: build or install a single Go executable.
- **Repository-aware agent loop**: prompt once, keep a REPL open, or use the TUI
  while the agent reads, edits, searches, and runs commands in the workspace.
- **Permissioned tool use**: file writes and shell commands pass through explicit
  modes, rules, and confirmation paths.
- **Resumable local history**: conversations and tool events are stored as JSONL
  sessions that can be resumed, exported, compacted, or audited.
- **Extensible from the repo**: instructions, custom commands, skills, hooks,
  MCP servers, plugins, and additional workspaces can live with the project.

## Status

Codog is pre-1.0. The core local workflow is usable, but compatibility details,
sandboxing behavior, editor integrations, and enterprise features are still
evolving.

Use the binary as the source of truth for the exact surface in your checkout:

```bash
codog capabilities
codog help
codog doctor
```

## Install

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

From a local checkout:

```bash
go build ./cmd/codog
```

Set a provider key before asking Codog to call a model:

```bash
export ANTHROPIC_API_KEY="sk-ant-..."
```

OpenAI-compatible providers can also be configured with `OPENAI_API_KEY`,
`CODOG_PROVIDER`, `CODOG_MODEL`, and config files.

## First Run

Start with a read-only repository summary:

```bash
codog --permission-mode read-only prompt "summarize this repository"
```

Then move into an interactive workflow:

```bash
codog repl
```

Common next steps inside an interactive session are `/status`, `/permissions`,
`/model`, `/context`, `/add-dir`, `/workspace`, and `/help`.

## Core Workflow

Codog uses the same loop for one-shot prompts, REPL sessions, and the Bubble Tea
TUI:

1. Load config, instructions, focused files, selected workspace paths, and
   session state.
2. Stream a provider response.
3. Execute approved tools for repository inspection, file edits, searches,
   shell commands, todos, MCP calls, or background work.
4. Append model and tool events to local JSONL session files.

The separation is intentional: provider IO, workspace IO, permissions, and
session storage are distinct pieces that can be tested and reasoned about.

## Configuration

Configuration is layered from broad defaults to local overrides:

1. user config under the Codog config home;
2. project config in `.codog.json`;
3. uncommitted local overrides in `.codog.local.json`;
4. environment variables and CLI flags.

Example project config:

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

Run `codog config paths` to see which files are active for a workspace.

## Safety Model

Codog treats repository access as sensitive by default.

`read-only` mode is appropriate for review and explanation tasks.
`workspace-write` allows project edits while keeping access scoped.
`prompt` asks before risky actions.
`danger-full-access` is explicit and should be reserved for trusted workflows.

The runtime also supports broad-workspace guards, command validation, audit
events, undo snapshots for file mutations, and allow/deny permission rules.

## Extensibility

Codog can be shaped by files in the repository:

- instruction files such as `AGENTS.md`, `CLAUDE.md`, `CLAW.md`, and
  `.codog/instructions.md`;
- slash commands backed by prompts or local workflows;
- Markdown skills with optional tool constraints;
- hooks for agent, tool, command, session, and notification events;
- MCP clients for configured stdio servers and a local MCP server mode;
- plugins that bundle commands, skills, agents, hooks, MCP servers, and tools.

Run `codog capabilities --json` when you need the complete machine-readable
surface for automation or parity checks.

## Project Layout

| Path | Purpose |
| --- | --- |
| `cmd/codog` | CLI entry point. |
| `internal/agent` | Agent loop, command surface, slash handling, and local tools. |
| `internal/config` | Config loading, merging, validation, and persistence. |
| `internal/anthropic` | Anthropic-compatible streaming client. |
| `internal/session` | JSONL session storage, resume, export, and compaction support. |
| `internal/mcp`, `internal/mcpserver` | MCP client and server support. |
| `internal/tui` | Bubble Tea terminal UI. |
| `internal/control`, `internal/bridge` | Remote control and editor bridge surfaces. |

## Development

The normal validation loop is:

```bash
go test ./...
go build ./cmd/codog
```

Keep generated local state, machine-specific paths, cache locations, and
tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the MIT License.
