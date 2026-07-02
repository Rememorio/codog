# Codog

Codog is a Go-native terminal coding agent inspired by Claude Code. It runs as
a single local binary and keeps the agent loop visible: prompts, model
streaming, tool calls, permissions, and session history are all implemented in
ordinary Go code in this repository.

It is built for developers who want a hackable coding agent they can inspect,
extend, and run locally. It is not an official Anthropic product, and it should
not be treated as a hardened sandbox for hostile repositories or untrusted
commands.

## Current Status

Codog is usable as an experimental local agent today. The core workflow supports
one-shot prompts, REPL and TUI sessions, Anthropic-compatible streaming, local
workspace tools, permission checks, JSONL session history, resume, layered
configuration, skills, hooks, MCP surfaces, provider routing, and diagnostics.

Some advanced surfaces are still compatibility work in progress. IDE bridge,
remote sessions, plugin workflows, OAuth, enterprise policy, updater, notebook,
LSP, background work, and parity harnesses exist in varying levels of depth and
should be validated before relying on them.

## Install

Codog requires Go 1.24.2 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

For local development:

```sh
go build ./cmd/codog
```

## First Run

Set an Anthropic key, then ask Codog to inspect the current repository.

```sh
export ANTHROPIC_API_KEY=<key>
codog -p "summarize this repository"
```

Start an interactive session when you want a longer edit loop:

```sh
codog repl
```

Resume the latest saved session:

```sh
codog --resume latest -p "continue from where we left off"
```

Run `codog help` for the full command reference. The README intentionally keeps
the command list short so it stays useful as a project entry point.

## How Codog Works

Each run follows the same basic loop:

```text
load instructions, config, and optional session history
ask the model and stream the response
receive tool requests from the model
check permissions and trust rules
run local tools
append the result to a JSONL session
```

The important pieces live under `internal/`:

- `internal/agent` owns CLI dispatch and the agent loop.
- `internal/tools` contains local read, write, edit, grep, glob, patch, shell,
  git, notebook, and code-intelligence tools.
- `internal/session` stores JSONL transcripts, summaries, export data, and
  resume metadata.
- `internal/config` loads user, project, local, and environment configuration.
- `internal/anthropic` implements the Anthropic-compatible client.
- `internal/tui`, `internal/mcp`, `internal/skills`, and `internal/hooks`
  provide the interactive and extension surfaces.

## Everyday Use

Use one-shot prompts for quick repository questions or small changes:

```sh
codog -p "find the config loading path and explain it"
```

Use the REPL or TUI for iterative work:

```sh
codog repl
codog tui
```

Inspect local health before debugging model or tool behavior:

```sh
codog doctor
codog status
codog providers status
```

Use session commands when work needs to be reviewed, exported, compacted, or
continued later:

```sh
codog sessions list
codog history --session latest
codog export --session latest --format markdown
codog compact --resume latest
```

## Configuration

Configuration is layered so repository defaults and personal preferences can
stay separate.

- `AGENTS.md` describes project instructions loaded into the agent context.
- `.codog.json` stores shared project configuration.
- `.codog.local.json` stores uncommitted local overrides.
- `.codog/commands` contains project slash commands.
- `.codog/skills` contains project skills.
- `.codog/hooks` contains project automation hooks.

Secrets should stay in environment variables or local-only config. Do not commit
API keys, local machine paths, generated session state, or private credentials.

## Providers

Anthropic is the default provider. Codog also includes OpenAI-compatible routing
and named provider shortcuts for local or third-party model endpoints.

Common credentials:

- `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`
- `OPENAI_API_KEY`
- `XAI_API_KEY`
- `DASHSCOPE_API_KEY`
- `OLLAMA_HOST`

Use `codog providers status` to see which provider and credential path are
active for the current run.

## Safety Model

Codog separates model intent from local execution. Tools declare what they want
to do, and the runtime checks the request against the active permission mode
before running it.

Common modes:

- `read-only` for inspection and planning.
- `workspace-write` for edits inside the current workspace.
- `prompt` for approval before sensitive actions.
- `danger-full-access` or `allow` for explicitly unrestricted local use.

These controls are development guardrails, not a security boundary. Review
commands and file edits carefully in unfamiliar repositories.

## Development

Run the standard validation from the repository root:

```sh
go test ./...
go build ./cmd/codog
```

Useful entry points:

- `cmd/codog/main.go` starts the CLI.
- `internal/agent/agent.go` is the main command and runtime dispatch layer.
- `internal/agent/agent_test.go` covers much of the CLI contract surface.
- `internal/mockanthropic` and `internal/harness` support offline compatibility
  testing.

Keep changes portable: avoid committing local absolute paths, generated caches,
API keys, machine-specific setup snippets, or tool-generated attribution text.

## License

Codog is released under the [MIT License](LICENSE).
