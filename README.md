# Codog

Codog is an experimental Go implementation of a Claude Code-style terminal
coding agent.

It runs as a single local binary, streams model responses, asks before sensitive
local actions, and records sessions as JSONL that can be inspected, resumed, or
compacted. The project is for developers who want a coding-agent workflow whose
moving parts are visible in the repository instead of hidden behind a hosted
service.

Codog is not an official Anthropic product, and it is not yet a hardened sandbox
for untrusted code.

## Install

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY=<key>
codog -p "summarize this repository"
```

For longer work, use the REPL or the Bubble Tea interface:

```sh
codog repl
codog tui
```

## What Codog Is

Codog keeps the agent loop close to the machine it is operating on:

- model streaming for Anthropic and OpenAI-compatible providers
- workspace tools for shell commands, file reads, writes, edits, grep, glob,
  patches, notebooks, git, and code-intelligence queries
- permission modes, allow and deny rules, audit records, and trust checks
- local JSONL sessions with resume, export, summaries, rewind, cost, usage, and
  compaction helpers
- slash commands, prompt templates, skills, hooks, MCP, plugins, and background
  tasks

The intent is not just to copy a product surface. The intent is to make a local,
Go-native agent runtime that is straightforward to debug, extend, and reason
about.

## Typical Workflow

In a repository, Codog usually does four things:

1. Load project instructions, configuration, memory, focused paths, and optional
   session history.
2. Stream a response from the selected model.
3. Check requested tool calls against the active permission mode.
4. Persist messages, approvals, tool calls, usage, summaries, and metadata to
   local session state.

Common entry points are intentionally small:

| Need | Start here |
| --- | --- |
| Ask one question | `codog -p "review this diff"` |
| Continue previous work | `codog --resume latest -p "finish the next step"` |
| Work interactively | `codog repl` or `codog tui` |
| Inspect local setup | `codog doctor` |
| See the compiled feature surface | `codog capabilities` |

Use `codog help` for the full command reference. The README stays focused on
orientation instead of duplicating the CLI manual.

## Configuration

Configuration is layered so repository defaults and personal choices can stay
separate.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Repository instructions loaded into the agent context |
| `.codog.json` | Shared project configuration |
| `.codog.local.json` | Uncommitted local overrides |
| `.codog/commands` | Project slash commands |
| `.codog/skills` | Project skills |
| `.codog/hooks` | Project automation hooks |

Secrets should live in environment variables or local-only config, not in files
committed to a repository.

## Providers

Anthropic is the default provider. OpenAI-compatible providers can be selected
through model names, aliases, and configuration.

| Provider | Typical selection | Credential |
| --- | --- | --- |
| Anthropic | default Claude model | `ANTHROPIC_API_KEY` |
| OpenAI-compatible | `openai/...` | `OPENAI_API_KEY` |
| xAI | `grok`, `grok-mini`, or `xai/...` | `XAI_API_KEY` |
| DashScope | `qwen...` or `qwen/...` | `DASHSCOPE_API_KEY` |
| Custom Anthropic-compatible | configured base URL | `ANTHROPIC_API_KEY` or `ANTHROPIC_AUTH_TOKEN` |

Run `codog providers status` to inspect the active provider configuration.

## Safety Model

Codog separates model intent from local execution. Tools declare the permission
they need, and the runtime checks the request before running commands, editing
files, or touching the wider system.

| Mode | Best for |
| --- | --- |
| `read-only` | Codebase inspection and planning |
| `workspace-write` | Normal edits inside the current workspace |
| `prompt` | Interactive confirmation before sensitive actions |
| `danger-full-access` or `allow` | Explicitly unrestricted local use |

These controls are development guardrails. Do not treat them as a security
boundary for hostile repositories or untrusted commands.

## Project Status

Codog is under active development. The core CLI, provider streaming, local tools,
sessions, permissions, REPL, TUI, slash commands, MCP, hooks, skills, usage
tracking, and plugin surfaces exist, but many advanced areas are still
compatibility-oriented and should be validated before serious use.

Codog is probably not the right tool today if you need vendor support, guaranteed
Claude Code parity, enterprise administration, or hardened cross-platform
isolation.

## Development

The CLI entry point is `cmd/codog`. Most implementation code lives under
`internal/`, split by responsibility: agent loop, tools, providers, sessions,
configuration, hooks, skills, plugins, MCP, background work, code intelligence,
and TUI.

```sh
go test ./...
go build ./cmd/codog
```

Keep generated state, secrets, local paths, and tool-attribution signatures out
of commits.

## License

Codog is released under the [MIT License](LICENSE).
