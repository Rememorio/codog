# Codog

Codog is a Go-native coding agent CLI. It is a clean-room implementation that
uses public API contracts, local tests, and its own runtime design instead of
translating Claude Code source.

The goal is a practical single-binary assistant for local development: prompt
it once, keep a REPL open, let it read and edit workspace files, run commands
behind an explicit permission model, resume sessions, and integrate with MCP,
hooks, plugins, and editor/remote bridges.

## Status

Codog is usable but still experimental. It implements a broad Claude Code-like
surface, but exact behavioral parity is an ongoing target. Prefer `codog
self-test`, `codog doctor`, and `codog capabilities --json` when you need to
verify what a build supports.

## Highlights

- Single Go binary with one-shot prompts, REPL, and Bubble Tea TUI.
- Anthropic Messages streaming plus OpenAI-compatible streaming for models
  prefixed with `openai/`.
- Workspace tools for shell commands, file read/write/edit, grep/glob, git,
  notebooks, lightweight Go code intelligence, tasks, todos, and background
  jobs.
- Session JSONL persistence with resume, rewind, export, summaries, token/cost
  accounting, prompt history, and automatic compaction.
- Explicit permission modes: `read-only`, `workspace-write`,
  `danger-full-access`, `prompt`, and `allow`.
- Extensibility through Markdown skills, custom slash commands, templates,
  hooks, MCP clients/servers, local plugins, and a signed plugin marketplace.
- Integration surfaces for ACP/Zed, editor bridges, local HTTP remote control,
  mobile/desktop handoff reports, and update workflows.

## Quick Start

Build from a checkout:

```bash
go build ./cmd/codog
export ANTHROPIC_API_KEY="sk-ant-..."
./codog prompt "summarize this repository"
./codog repl
```

Install with Go:

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
```

Common first commands:

```bash
codog doctor
codog prompt "inspect the project and suggest the next test to run"
codog repl
codog tui
codog --resume latest repl
codog capabilities --json
```

Use a different provider:

```bash
codog providers set anthropic
codog providers set openai --model openai/gpt-4o-mini
codog providers set custom --base-url http://127.0.0.1:8000 --model openai/local
```

## Core Workflows

**Ask once or keep a session open**

Use `codog prompt` for a one-shot request, `codog repl` for an interactive
session, and `codog tui` for a terminal UI prompt composer. Sessions are saved
as JSONL and can be resumed with `--resume latest` or a session ID.

**Work safely in a repository**

Codog scopes file tools to the workspace and configured extra directories. It
can read images, edit files with undo snapshots, search with grep/glob, run
shell commands, and manage background jobs. Broad working directories such as
home and filesystem root are guarded unless `--allow-broad-cwd` is passed.

**Control permissions explicitly**

Start conservative:

```bash
codog --permission-mode read-only prompt "review the diff"
codog --permission-mode prompt prompt "fix the failing test"
codog --allowed-tools "Read" --allowed-tools "Bash(go test:*)" prompt "verify"
```

Compatibility aliases such as `Bash`, `Read`, `Write`, `Edit`, `GrepSearch`,
`GlobSearch`, `Task`, and `TodoWrite` are accepted while Codog keeps canonical
tool names internally.

**Keep context manageable**

Project memory is loaded from `AGENTS.md`, `CLAUDE.md`,
`.claude/CLAUDE.md`, `CLAW.md`, and `.codog/instructions.md`. You can add
focused paths with `codog focus`, reference files with `@path`, compact long
sessions with `codog compact`, and inspect context before a provider request
with `codog context`.

**Automate repeatable work**

Use `codog background`, `codog tasks`, `codog cron`, and `codog team` for local
background prompts and multi-task coordination. Hooks can run on session,
permission, tool, compaction, notification, worktree, task, instruction-load,
cwd-change, and file-change events.

## Extensibility

Codog looks for user and workspace extension files under `~/.codog`, `.codog`,
and compatible `.claude` directories:

- `skills/` for Markdown skills with metadata and optional allowed tools.
- `commands/` for custom slash commands and reusable local workflows.
- `templates/` for parameterized prompts.
- `hooks/hooks.json` and config `hooks` for command, HTTP, prompt, and agent
  hooks.
- plugin directories with commands, skills, agents, hooks, MCP servers, and
  tools.

MCP support is available both ways: Codog can call configured stdio MCP servers,
and `codog mcp serve` exposes Codog's local tools over stdio MCP.

## Configuration

Configuration is merged from global, project, local, environment, and flags:

- `~/.codog/config.json`
- `.codog.json`
- `.codog.local.json`
- environment variables such as `ANTHROPIC_API_KEY`, `CODOG_MODEL`,
  `CODOG_PERMISSION_MODE`, `OPENAI_API_KEY`, and `CODOG_CONFIG_HOME`

Inspect and edit config through the CLI:

```bash
codog config
codog config paths
codog config set permission_mode workspace-write --target project
codog config unset permission_rules.deny --target project
```

Minimal example:

```json
{
  "model": "claude-sonnet-4-5",
  "permission_mode": "workspace-write",
  "max_turns": 8,
  "max_tokens": 4096,
  "additional_dirs": ["../shared"],
  "permission_rules": {
    "deny": ["bash:rm -rf"]
  },
  "mcp_servers": {
    "example": {
      "command": "example-mcp-server",
      "args": []
    }
  }
}
```

Useful preference commands include `codog theme`, `codog vim`, `codog voice`,
`codog chrome`, `codog privacy-settings`, `codog keybindings`, and
`codog output-style`.

## Integrations

- `codog acp serve` starts an ACP/Zed-compatible stdio JSON-RPC server.
- `codog bridge serve` and `codog remote-control serve` expose trusted editor
  bridge operations.
- `codog remote serve [addr]` starts a local HTTP control API for sessions,
  background tasks, workspace files, code intelligence, editor state, hook
  health, auth, and heartbeat lease state.
- `codog terminal-setup` installs or removes shell integration snippets.
- `codog updater` verifies and installs signed release artifacts.
- `codog install-github-app` can generate Claude Code GitHub Actions workflow
  files.

## Finding Commands

The README is intentionally not the command reference. Use the built-in
discovery commands instead:

```bash
codog help
codog help prompt
codog capabilities
codog capabilities --json
codog commands list
codog skills list
```

Inside the REPL, use `/help`, `/tool-details bash`, `/commands`, and `/skills`
for the matching interactive views.

For local validation:

```bash
codog doctor
codog self-test
go test ./...
```

## Development Notes

This repository targets Go 1.24+. The test suite includes unit tests, local
integration tests, and a mock provider parity harness. Most development changes
should at least pass:

```bash
go test ./...
```

Generated local state lives under `.codog/` and is ignored by default.
