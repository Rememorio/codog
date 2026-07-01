# Codog

Codog is a Go-native coding-agent CLI for working inside a repository from the
terminal. It streams model responses, lets the agent inspect and edit files,
runs shell commands behind permission gates, and records local sessions as JSONL
so work can be resumed, reviewed, or compacted later.

Codog is an independent, clean-room Claude Code-like implementation. It is not
affiliated with Anthropic. Claude Code parity is a compatibility goal, not a
claim that every edge case is already identical.

## Status

Codog is pre-1.0 software. The core local workflow is usable, but command
compatibility, sandbox behavior, IDE integration, and enterprise surfaces are
still evolving.

It is a good fit today for:

- running one-shot coding-agent tasks from a terminal;
- keeping an interactive REPL or TUI session in a repository;
- experimenting with local-first agent primitives such as permissions,
  resumable sessions, skills, hooks, and MCP;
- validating compatibility behavior against a Go implementation that is easy to
  inspect and ship as one binary.

For the exact capability set of the binary you are running, prefer runtime
introspection over README prose:

```bash
codog doctor
codog capabilities --json
codog help
```

## Quick Start

Codog requires Go 1.24 or newer.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
codog prompt "summarize this repository"
```

From a checkout:

```bash
go build ./cmd/codog
./codog repl
```

OpenAI-compatible providers are also supported through configuration and
environment variables such as `OPENAI_API_KEY`, `CODOG_MODEL`, and
`CODOG_PROVIDER`.

## How Codog Works

Codog follows the same basic loop for one-shot prompts, the REPL, and the TUI:

1. Load instructions, config, session state, focused paths, and explicit
   `@file` references from the current workspace.
2. Stream a model response from Anthropic Messages or an OpenAI-compatible
   provider.
3. Execute approved tools for reading, writing, editing, searching, running
   commands, managing todos, or delegating background work.
4. Persist the conversation and tool events to local JSONL so the session can be
   resumed, exported, rewound, audited, or compacted.

This keeps the runtime understandable: model IO, workspace IO, permissions, and
session storage are separate concerns instead of one opaque service.

## Everyday Workflow

| Need | Use |
| --- | --- |
| Ask once or make a focused change | `codog prompt "..."` |
| Iterate with memory in the terminal | `codog repl` |
| Use a richer terminal interface | `codog tui` |
| Continue previous work | `codog --resume latest repl` |
| Inspect the current build surface | `codog capabilities` |

Inside interactive sessions, slash commands provide the common controls for
model choice, permissions, tools, compaction, session state, skills, hooks, and
configuration. The command list is intentionally kept in the binary so it stays
accurate as the implementation changes.

## Safety Model

Codog assumes repository access is sensitive. File and shell tools pass through
permission checks before they run, and the narrowest useful mode is usually the
right default for a task.

```bash
codog --permission-mode read-only prompt "review this diff"
codog --permission-mode prompt prompt "fix the failing test"
```

Supported modes include read-only inspection, workspace write access,
prompt-based approval, allow-list based execution, and explicit full-access
operation. Tool aliases compatible with Claude Code-style names are accepted at
the CLI boundary while Codog keeps canonical tool names internally.

Codog also records audit events, supports undo snapshots for file mutations,
guards broad directory access, and validates risky shell commands before they are
allowed to run.

## Configuration

Configuration is layered so personal defaults, project policy, and local
overrides can coexist:

1. user config under `~/.codog/config.json`;
2. project config in `.codog.json`;
3. local, uncommitted overrides in `.codog.local.json`;
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

Use `codog config paths` to see which files are active for a workspace.

## Extensibility

Codog is designed to be extended from the repository, not only from global
machine state.

- **Instructions** are loaded from common project files such as `AGENTS.md`,
  `CLAUDE.md`, `.claude/CLAUDE.md`, `CLAW.md`, and `.codog/instructions.md`.
- **Skills** package reusable Markdown guidance with optional tool
  constraints.
- **Custom commands** turn prompts and workflows into slash commands.
- **Hooks** connect command, HTTP, prompt, and agent lifecycle events to local
  automation.
- **MCP** lets Codog call configured stdio MCP servers, and `codog mcp serve`
  exposes Codog's local tools over stdio MCP.
- **Plugins** can bundle commands, skills, agents, hooks, MCP servers, and local
  tools behind a single installable unit.

## Project Map

| Path | Responsibility |
| --- | --- |
| `cmd/codog` | CLI entry point. |
| `internal/agent` | Command surface, agent orchestration, tools, and slash handling. |
| `internal/config` | Config loading, merging, validation, and persistence. |
| `internal/anthropic` | Anthropic-compatible streaming client. |
| `internal/session` | JSONL session storage, resume, and export. |
| `internal/mcp`, `internal/mcpserver` | MCP client and server support. |
| `internal/tui` | Bubble Tea terminal UI. |
| `internal/control`, `internal/bridge` | Remote control and editor bridge surfaces. |

## Development

The normal validation loop is intentionally small:

```bash
go test ./...
go build ./cmd/codog
```

Generated local state is written under `.codog/` and ignored by default. Keep
user-specific paths, cache locations, generated attribution, and local machine
details out of code, docs, commit messages, and examples.

## License

Codog is released under the MIT License.
