# Codog

Codog is an experimental Go-native coding agent that runs from your terminal.
It keeps the important parts of an agent runtime in one inspectable binary:
model streaming, workspace tools, permissions, local sessions, hooks, skills,
MCP, and interactive shells.

Codog is inspired by Claude Code, but it is not an Anthropic product and does
not try to present itself as one. The goal is to build a practical local agent
while keeping the implementation understandable enough to study, modify, and
extend.

Codog is most useful today if you want:

- a terminal coding agent with local, file-backed state;
- a Go codebase that shows how agent loops and tool execution fit together;
- explicit permission checks before model-requested actions affect a workspace;
- room to experiment with Claude-Code-style workflows without a multi-runtime
  stack.

It is not yet a polished drop-in replacement for commercial coding agents.

## Quick Start

Codog requires Go 1.24.2 or newer and at least one model credential.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY=<key>
```

Run it from a repository:

```sh
codog -p "summarize this project"
```

For multi-turn work, use an interactive shell:

```sh
codog repl
codog tui
```

Use `codog doctor` to check local configuration and `codog help` for the full
command reference.

## Current State

Codog is usable for local experimentation and ordinary repository workflows.
The strongest surfaces are:

- one-shot prompts, REPL, Bubble Tea TUI, text output, JSON output, and streaming
  JSON output;
- Anthropic-compatible streaming, plus configurable routes for
  OpenAI-compatible APIs, Ollama, xAI, DashScope, and custom base URLs;
- local tools for shell commands, file reads and writes, search, edits, git,
  notebooks, and code intelligence;
- JSONL sessions, resume, history, rewind, summaries, exports, usage reporting,
  and compaction;
- permission modes, allow and deny rules, hooks, audit events, and basic sandbox
  toggles;
- slash commands, skills, templates, MCP client/server paths, provider profiles,
  background tasks, and bridge surfaces.

Some larger surfaces exist but should still be treated as implementation
workbench code until you validate them in your environment: remote sessions, IDE
bridge behavior, OAuth, enterprise policy, marketplace flows, updater flows,
multi-agent orchestration, and cross-platform sandbox integrations.

## Design Principles

Codog is built around a few constraints.

Local first. Sessions, summaries, usage data, todos, exports, and most runtime
state are plain local files rather than hidden service state.

Explicit execution. The model can request tools, but the runtime decides whether
they are allowed. Permission mode, workspace boundaries, allow and deny lists,
hooks, and policy checks all happen before host actions run.

One binary. The CLI, REPL, TUI, provider client, tool runtime, MCP surfaces,
skills, hooks, background jobs, and bridge helpers are implemented in Go and
ship as a single command.

Readable internals. The project favors ordinary packages and file-backed data
formats over opaque generated state, so the runtime can be debugged with normal
tools.

## How It Works

A Codog turn follows the same loop whether it starts from a one-shot prompt,
REPL, TUI, or bridge call:

1. Load configuration, project instructions, focused files, and optional session
   history.
2. Stream a request to the configured model provider.
3. Parse assistant text and requested tool calls.
4. Check permissions, workspace boundaries, allow and deny rules, hooks, and
   local policy.
5. Execute approved tools and append the result to the session ledger.
6. Continue until the model finishes, a configured limit is reached, or the user
   stops the run.

The session ledger is JSONL, so runs can be inspected, resumed, compacted, or
exported without a database.

## Configuration

Codog separates shared project defaults from personal settings and local
secrets.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions loaded into the agent context |
| `.codog.json` | Shared project configuration |
| `.codog.local.json` | Uncommitted local overrides |
| `.codog/commands` | Project slash commands |
| `.codog/skills` | Project skills |
| `.codog/hooks` | Project hook scripts |

Common provider variables:

- `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`
- `ANTHROPIC_BASE_URL` or `CODOG_BASE_URL`
- `OPENAI_API_KEY` and `OPENAI_BASE_URL`
- `OLLAMA_HOST`
- `XAI_API_KEY`
- `DASHSCOPE_API_KEY`

Keep credentials in environment variables or local-only config. Do not commit
API keys, generated sessions, caches, private prompts, or machine-specific
paths.

## Safety Model

Codog separates assistant intent from host execution.

Permission modes include `read-only`, `workspace-write`, `prompt`,
`danger-full-access`, and `allow`. The default mode should match how much trust
you want to give a run: inspect-only work belongs in `read-only`; edits inside a
repository belong in `workspace-write`; sensitive tool use should go through
`prompt`.

These controls are workflow guardrails. They are not a complete security sandbox
for hostile repositories, untrusted commands, or adversarial prompts.

## Repository Map

| Path | Responsibility |
| --- | --- |
| `cmd/codog` | CLI entry point |
| `internal/agent` | Command dispatch, runtime wiring, and the agent loop |
| `internal/anthropic` | Anthropic-compatible client and message types |
| `internal/tools` | Shell, file, search, git, and edit tools |
| `internal/session` | JSONL transcripts, resume, export, and metadata |
| `internal/config` | User, project, local, and environment configuration |
| `internal/tui` | Bubble Tea interactive interface |
| `internal/mcp` | MCP client integration |
| `internal/skills` | Skill discovery and activation |
| `internal/hooks` | Local automation hooks |
| `internal/control` | Remote control and IDE bridge surfaces |

## Development

The normal validation path is intentionally boring:

```sh
go test ./...
go build ./cmd/codog
```

Keep changes portable. Avoid committing generated caches, API keys,
machine-specific setup snippets, local absolute paths, or tool attribution text.

## License

Codog is released under the [MIT License](LICENSE).
