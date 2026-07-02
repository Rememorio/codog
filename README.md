# Codog

Codog is a Go-native coding agent CLI for running local, inspectable coding
workflows from one binary.

It is inspired by Claude Code, but it is not an official Anthropic product. The
goal is to make the agent runtime easy to study and extend: provider streaming,
workspace tools, permission checks, session history, slash commands, skills, MCP,
hooks, and interactive shells live in one Go codebase.

## Why Codog

Most coding agents are hard to reason about because the runtime is split across
provider adapters, UI shells, local tools, hidden state, and remote services.
Codog keeps the important pieces close together:

- **One binary:** build or install a single CLI and run it inside any repository.
- **Local-first state:** sessions, summaries, usage, todos, and exports are local
  files instead of opaque remote history.
- **Explicit execution:** model-requested tools pass through permission modes,
  allow/deny rules, hooks, and audit events before touching the workspace.
- **Hackable surface:** the same command surface covers REPL, TUI, slash
  commands, skills, MCP, background jobs, and provider configuration.

Codog is useful if you want a practical coding assistant today and a readable
implementation of how one can be built.

## Status

Codog is usable for experimentation and local agent workflows. The one-shot
prompt, REPL, Bubble Tea TUI, Anthropic-compatible streaming, workspace tools,
JSONL sessions, config layering, permissions, skills, hooks, MCP, usage tracking,
and compatibility harnesses are implemented.

Some surfaces are intentionally still moving. Remote sessions, IDE bridge work,
OAuth, enterprise policy, plugin marketplace, updater behavior, and sandbox
integration should be treated as experimental until you have validated them for
your environment.

## Install

Codog requires Go 1.24.2 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

Set a provider credential before asking the model to do real work:

```sh
export ANTHROPIC_API_KEY=<key>
```

Codog also has configuration paths for OpenAI-compatible endpoints, Ollama, xAI,
DashScope, and custom Anthropic-compatible base URLs.

## Quick Start

Ask a one-shot question from a repository:

```sh
codog -p "summarize this project"
```

Use an interactive surface for multi-turn work:

```sh
codog repl
codog tui
```

Resume recent context when you want the agent to continue from local history:

```sh
codog --resume latest -p "continue the refactor"
```

Pipe input when another command already produced the context:

```sh
git diff --staged | codog -p "review this patch" --stdin
```

Run `codog help` for the full command reference.

## Core Concepts

Codog runs each agent turn through a small set of explicit stages:

1. Load project instructions, config, focused files, and optional session
   history.
2. Send a streaming request to the configured model provider.
3. Parse assistant text and requested tool calls.
4. Check permissions, allowed tools, hooks, and local policy.
5. Execute approved tools and append results to the session ledger.
6. Continue until the model finishes, a limit is reached, or the user stops the
   run.

The important design choice is that a model can ask for local actions, but the
runtime decides whether those actions are allowed.

## What It Includes

| Area | What Codog provides |
| --- | --- |
| Interfaces | One-shot prompts, REPL, Bubble Tea TUI, JSON and streaming JSON output |
| Providers | Anthropic-compatible streaming plus configurable provider routes |
| Workspace tools | Shell, file read/write/edit, grep, glob, git, notebook, and code-intel helpers |
| Session state | JSONL transcripts, resume, rewind, summary, export, usage, and compaction |
| Controls | Permission modes, allowed tools, hooks, audit events, and sandbox toggles |
| Extensions | Slash commands, skills, MCP client/server surfaces, plugins, and templates |
| Automation | Background tasks, cron-style jobs, team/agent experiments, and remote bridge surfaces |

The README stays intentionally high level. The CLI is the source of truth for
the full command surface:

```sh
codog help
codog doctor
codog status
```

## Configuration

Configuration is layered so project defaults, personal preferences, and local
secrets can stay separate.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions loaded into the agent context |
| `.codog.json` | Shared project configuration |
| `.codog.local.json` | Uncommitted local overrides |
| `.codog/commands` | Project slash commands |
| `.codog/skills` | Project skills |
| `.codog/hooks` | Project hook scripts |

Use environment variables or local-only config for credentials. Do not commit
API keys, generated sessions, caches, private prompts, or local machine paths.

Common provider environment variables:

- `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`
- `ANTHROPIC_BASE_URL` or `CODOG_BASE_URL`
- `OPENAI_API_KEY` and `OPENAI_BASE_URL`
- `OLLAMA_HOST`
- `XAI_API_KEY`
- `DASHSCOPE_API_KEY`

## Safety Model

Codog separates assistant intent from local execution. Tool requests are checked
against the active permission mode, workspace, allow/deny rules, hooks, and
runtime policy before they run.

Common permission modes:

- `read-only` for inspection and planning.
- `workspace-write` for edits inside the current workspace.
- `prompt` for approval before sensitive actions.
- `danger-full-access` or `allow` for explicitly unrestricted local use.

These controls are workflow guardrails, not a complete security sandbox for
hostile repositories or untrusted commands.

## Development

The repository is organized around the runtime boundaries:

| Path | Responsibility |
| --- | --- |
| `cmd/codog` | CLI entry point |
| `internal/agent` | Command dispatch, runtime wiring, and agent loop |
| `internal/anthropic` | Anthropic-compatible client and message types |
| `internal/tools` | Workspace tools for shell, files, search, git, and edits |
| `internal/session` | JSONL transcripts, resume, export, and metadata |
| `internal/config` | Layered user, project, local, and environment config |
| `internal/tui` | Bubble Tea interactive interface |
| `internal/mcp` | MCP client integration |
| `internal/skills` | Skill discovery and activation |
| `internal/hooks` | Local automation hooks |
| `internal/control` | Remote control and IDE bridge HTTP surface |

Build and test from the repository root:

```sh
go build ./cmd/codog
go test ./...
```

Keep changes portable. Avoid committing local absolute paths, generated caches,
API keys, machine-specific setup snippets, or tool attribution text.

## License

Codog is released under the [MIT License](LICENSE).
