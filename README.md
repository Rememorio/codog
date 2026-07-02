# Codog

Codog is a Go-native coding agent CLI for local, inspectable software work.

It is inspired by Claude Code, but it is not an official Anthropic product and
is not intended to pretend otherwise. The project exists to make the core agent
runtime understandable: streaming model calls, workspace tools, permission
checks, sessions, skills, hooks, MCP, and interactive shells live in one Go
codebase.

```sh
codog -p "review the staged diff"
codog tui
codog --resume latest -p "continue the refactor"
```

## Why Codog

Most coding agents hide important behavior behind remote services, generated
state, or UI-specific glue. Codog keeps those pieces close to the repository:

- **One binary:** install a single CLI and run it inside the project you want to
  inspect or edit.
- **Local state:** sessions, summaries, usage data, todos, and exports are plain
  local files.
- **Explicit execution:** model-requested tools pass through permission modes,
  allow/deny rules, hooks, and audit events before they affect the workspace.
- **Hackable runtime:** the CLI, REPL, TUI, MCP surfaces, skills, hooks, and
  background jobs are implemented in Go instead of split across several runtimes.

Use Codog if you want a practical terminal agent and a readable implementation
of how one can be built. Do not treat it as a finished drop-in replacement for
commercial coding agents yet.

## Status

Codog is usable for local experimentation and day-to-day agent workflows, but the
project is still maturing. The most reliable surfaces are one-shot prompts, REPL,
TUI, provider streaming, local workspace tools, JSONL sessions, permission
checks, configuration, skills, hooks, MCP, and usage reporting.

Remote sessions, IDE bridge behavior, OAuth, enterprise policy, marketplace
support, updater flows, and sandbox integrations are present as implementation
surfaces but should be validated in your own environment before depending on
them.

## Install

Codog requires Go 1.24.2 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

Set at least one model credential before running agent requests:

```sh
export ANTHROPIC_API_KEY=<key>
```

Anthropic-compatible endpoints are the primary path. The configuration layer also
has routes for OpenAI-compatible APIs, Ollama, xAI, DashScope, and custom base
URLs.

## First Run

Start in a repository and ask a focused question:

```sh
codog -p "summarize this project"
```

Switch to an interactive surface when the task needs several turns:

```sh
codog repl
codog tui
```

Resume local history when the next request depends on previous context:

```sh
codog --resume latest -p "continue"
```

The README is intentionally not the command manual. Use `codog help` and
`codog doctor` for the full generated reference and environment checks.

## What It Provides

| Area | Current shape |
| --- | --- |
| Agent surfaces | One-shot prompt, REPL, Bubble Tea TUI, JSON output, streaming JSON output |
| Model routing | Anthropic-compatible streaming with configurable provider routes |
| Workspace tools | Bash, read, write, edit, grep, glob, git helpers, notebooks, and code intelligence |
| Sessions | JSONL transcripts, resume, history, rewind, summary, export, usage, and compaction |
| Controls | Permission modes, allowed and denied tools, hooks, audit events, and sandbox toggles |
| Extensions | Slash commands, skills, templates, MCP client/server paths, plugins, and provider profiles |
| Automation | Background tasks, cron-style jobs, team/agent experiments, and bridge surfaces |

## How It Works

Each agent turn follows the same basic loop:

1. Load project instructions, config, focused files, and optional session
   history.
2. Stream a request to the configured model provider.
3. Parse assistant text and requested tool calls.
4. Check permissions, workspace boundaries, allow/deny rules, hooks, and local
   policy.
5. Execute approved tools and append results to the session ledger.
6. Continue until the model finishes, a limit is reached, or the user stops the
   run.

The model can ask for local actions, but the runtime decides whether those
actions are allowed.

## Configuration Model

Codog separates shared project defaults from personal preferences and local
secrets.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Project instructions loaded into the agent context |
| `.codog.json` | Shared project configuration |
| `.codog.local.json` | Uncommitted local overrides |
| `.codog/commands` | Project slash commands |
| `.codog/skills` | Project skills |
| `.codog/hooks` | Project hook scripts |

Keep credentials in environment variables or local-only config. Do not commit API
keys, generated sessions, caches, private prompts, or machine-specific paths.

Common provider variables:

- `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN`
- `ANTHROPIC_BASE_URL` or `CODOG_BASE_URL`
- `OPENAI_API_KEY` and `OPENAI_BASE_URL`
- `OLLAMA_HOST`
- `XAI_API_KEY`
- `DASHSCOPE_API_KEY`

## Safety Model

Codog separates assistant intent from host execution. Tool requests are checked
against the active permission mode, workspace boundary, allow/deny rules, hooks,
and runtime policy before they run.

Common permission modes:

- `read-only` for inspection and planning.
- `workspace-write` for edits inside the current workspace.
- `prompt` for approval before sensitive actions.
- `danger-full-access` or `allow` for explicitly unrestricted local use.

These controls are workflow guardrails. They are not a complete security sandbox
for hostile repositories, untrusted commands, or adversarial prompts.

## Repository Map

| Path | Responsibility |
| --- | --- |
| `cmd/codog` | CLI entry point |
| `internal/agent` | Command dispatch, runtime wiring, and agent loop |
| `internal/anthropic` | Anthropic-compatible client and message types |
| `internal/tools` | Shell, file, search, git, and edit tools |
| `internal/session` | JSONL transcripts, resume, export, and metadata |
| `internal/config` | User, project, local, and environment config |
| `internal/tui` | Bubble Tea interactive interface |
| `internal/mcp` | MCP client integration |
| `internal/skills` | Skill discovery and activation |
| `internal/hooks` | Local automation hooks |
| `internal/control` | Remote control and IDE bridge surfaces |

## Development

Build and test from the repository root:

```sh
go build ./cmd/codog
go test ./...
```

Keep changes portable. Avoid committing generated caches, local absolute paths,
API keys, machine-specific setup snippets, or tool attribution text.

## License

Codog is released under the [MIT License](LICENSE).
