# Codog

Codog is a Go-native terminal coding agent inspired by Claude Code. It runs as
a single local binary, streams model responses, executes workspace tools behind
permission checks, and records sessions as JSONL so work can be resumed,
reviewed, or exported later.

The project is for developers who want a hackable, inspectable coding agent
they can build and adapt locally. It is not an official Anthropic product, and
its safety controls are development guardrails rather than a hardened sandbox
for hostile repositories.

## At A Glance

- Single Go binary with one-shot, REPL, and Bubble Tea TUI entry points.
- Anthropic-compatible streaming, plus provider routing for OpenAI-compatible
  and local endpoints.
- Local tools for shell commands, file reads and edits, search, git, notebook
  operations, and code intelligence.
- Permission modes, allowed tool rules, audit events, and optional sandbox
  runtime checks.
- JSONL session history with resume, rewind, summaries, export, cost, usage,
  and compaction surfaces.
- Extensibility through slash commands, skills, hooks, plugins, MCP client and
  server surfaces, background tasks, agents, and IDE bridge experiments.

## Status

Codog is usable as an experimental local agent. The core terminal workflow is
implemented, and many Claude Code-compatible surfaces exist, but advanced areas
such as IDE bridge, remote sessions, OAuth, enterprise policy, updaters, plugin
distribution, and cross-platform sandboxing should be treated as compatibility
work in progress until validated in your environment.

## Install

Codog requires Go 1.24.2 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

For local development:

```sh
go build ./cmd/codog
```

## Quick Start

Set a provider credential:

```sh
export ANTHROPIC_API_KEY=<key>
```

Ask a one-shot question from a repository:

```sh
codog -p "summarize this repository"
```

Start an interactive loop when the task needs multiple turns:

```sh
codog repl
```

Resume recent work:

```sh
codog --resume latest -p "continue from where we left off"
```

Run `codog help` for the full command reference. The README stays intentionally
small so it remains useful as a project entry point.

## Common Workflows

| Workflow | Entry point |
| --- | --- |
| Quick repository question | `codog -p "explain the config loader"` |
| Interactive editing session | `codog repl` or `codog tui` |
| Health and environment checks | `codog doctor`, `codog status` |
| Session review | `codog sessions list`, `codog history --session latest` |
| Export or share work | `codog export --session latest --format markdown` |
| Provider inspection | `codog providers status` |
| Extensions | `codog skills list`, `codog commands list`, `codog mcp list` |

## How It Works

Every agent turn follows the same basic pipeline:

1. Load instructions, configuration, focused files, and optional session
   history.
2. Send a provider request and stream assistant output.
3. Parse tool requests from the model response.
4. Check permissions, allowed tools, and local safety rules.
5. Execute approved tools and append results to the session log.
6. Continue until the model finishes, a limit is reached, or the user stops the
   run.

Important packages:

| Path | Responsibility |
| --- | --- |
| `cmd/codog` | CLI entry point |
| `internal/agent` | Command dispatch, runtime wiring, and agent loop |
| `internal/anthropic` | Anthropic-compatible client and message types |
| `internal/tools` | Workspace tools for shell, files, search, git, and edits |
| `internal/session` | JSONL transcripts, resume, export, and session metadata |
| `internal/config` | Layered user, project, local, and environment config |
| `internal/tui` | Bubble Tea interactive interface |
| `internal/mcp` | MCP client/server integration |
| `internal/skills` | Skill discovery and activation |
| `internal/hooks` | Local automation hooks |

## Configuration

Configuration is layered so shared project defaults and personal preferences can
stay separate.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions loaded into the agent context |
| `.codog.json` | Shared project configuration |
| `.codog.local.json` | Uncommitted local overrides |
| `.codog/commands` | Project slash commands |
| `.codog/skills` | Project skills |
| `.codog/hooks` | Project hook scripts |

Common provider environment variables:

- `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`
- `OPENAI_API_KEY`
- `OPENAI_BASE_URL`
- `XAI_API_KEY`
- `DASHSCOPE_API_KEY`
- `OLLAMA_HOST`

Keep secrets in environment variables or local-only config. Do not commit API
keys, generated session state, local cache paths, or private credentials.

## Safety Model

Codog separates model intent from local execution. A model can request a tool,
but the runtime decides whether that tool is allowed for the current permission
mode and workspace.

Common modes:

- `read-only` for inspection and planning.
- `workspace-write` for edits inside the current workspace.
- `prompt` for approval before sensitive actions.
- `danger-full-access` or `allow` for explicitly unrestricted local use.

Review commands and file edits carefully when working in unfamiliar
repositories. Permission checks reduce accidental damage, but they are not a
complete security boundary.

## Development

Run the standard validation from the repository root:

```sh
go test ./...
go build ./cmd/codog
```

The test suite includes offline model clients and compatibility harnesses, so
most CLI, session, tool, and extension behavior can be exercised without calling
real provider APIs.

Keep contributions portable: avoid committing local absolute paths, generated
caches, API keys, machine-specific setup snippets, or AI-tool attribution text.

## License

Codog is released under the [MIT License](LICENSE).
