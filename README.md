# Codog

Codog is a Go-native coding agent CLI for local software projects.

It is built as a single binary that can inspect a repository, talk to a
provider-backed model, run approved local tools, edit files, and resume saved
sessions. The project is compatibility-oriented: it aims to reproduce the
observable workflow of modern terminal coding agents while keeping the
implementation portable and inspectable.

> **Status:** pre-1.0. Codog is useful for experiments and local automation, but
> it is not yet a drop-in replacement for mature commercial coding agents.
> Advanced surfaces may be shallow, compatibility details may change, and the
> command line is still being tightened.

## Why Codog

Codog is intentionally small in deployment and explicit in behavior.

- **Single binary:** no separate runtime service is required for normal CLI use.
- **Local-first state:** sessions, approvals, config, hooks, and audit records
  live on disk where they can be inspected.
- **Permission-aware tools:** file access, shell commands, and workspace
  mutations pass through a configurable approval model.
- **Compatibility surface:** sessions, slash commands, skills, hooks, MCP,
  plugins, cost tracking, and editor-facing bridge pieces are modeled around
  observable behavior rather than private internals.
- **Go codebase:** core behavior lives in focused packages under `internal/`,
  with tests for command contracts and local subsystems.

## Install

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

Configure a provider key before the first model-backed run:

```sh
export ANTHROPIC_API_KEY=<key>
```

OpenAI-compatible endpoints can be configured through `CODOG_BASE_URL`,
`CODOG_MODEL`, and related config fields.

## First Run

Ask a one-shot question:

```sh
codog -p "explain this repository"
```

Start an interactive session:

```sh
codog repl
```

Use the terminal UI:

```sh
codog tui
```

Check local readiness without making a provider request:

```sh
codog doctor
```

For the full command surface, use `codog --help`. For a build-specific feature
summary, use `codog capabilities`.

## Core Workflow

Codog treats an agent session as local development work that should be easy to
review.

1. It loads repository guidance, memory, config, focused paths, and session
   history.
2. It streams a provider response and exposes local tools through explicit
   permission checks.
3. It records messages, tool calls, approvals, usage, and summaries in JSONL
   session state.
4. It can resume, export, compact, or inspect that state later.

The safest starting mode for an unfamiliar repository is read-only:

```sh
codog --permission-mode read-only -p "map the project and identify the test commands"
```

## Current Surface

The implementation already covers the main coding-agent loop and many
compatibility entry points. Depth varies by subsystem, so treat the test suite,
`codog capabilities`, and command-specific help as the source of truth for exact
behavior.

| Area | What exists today |
| --- | --- |
| Agent entry points | One-shot prompts, quick questions, REPL, and Bubble Tea TUI. |
| Providers | Anthropic Messages streaming and OpenAI-compatible streaming configuration. |
| Local tools | Bash, file read/write/edit, patching, grep/glob, git helpers, notebook helpers, and code-intelligence commands. |
| Sessions | JSONL persistence, resume, history, summaries, export/share/copy, usage, tokens, cost, cache stats, and compaction. |
| Configuration | Layered defaults, environment variables, user config, project config, local overrides, and CLI flags. |
| Extensibility | Slash commands, prompt templates, skills, hooks, MCP client/server pieces, plugins, and marketplace metadata. |
| Operations | Doctor/status reports, audit records, sandbox detection, background tasks, cron-style prompts, updater checks, and policy reports. |
| Integrations | Editor/ACP bridge state, remote-control surfaces, OAuth profiles, GitHub workflow helpers, and multi-agent/worktree scaffolding. |

Some advanced areas are compatibility scaffolds: they return structured local
reports and preserve command contracts before the underlying integrations are as
complete as the core prompt/tool/session loop.

## Configuration

Configuration is layered so team defaults and personal preferences can stay
separate.

| Location | Purpose |
| --- | --- |
| `AGENTS.md` | Repository instructions loaded into working context. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Uncommitted local overrides. |
| `.codog/commands` | Project slash commands. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Project hooks and local automation. |

Secrets belong in environment variables or local config, not in committed
project files.

## Safety Model

Codog separates intent from execution.

- Permission modes include read-only, prompt-gated workspace writes, and
  explicit unrestricted execution.
- Allowed and denied tool lists can narrow what the agent may call.
- Workspace scope, trusted roots, broad working-directory guards, and sandbox
  detection reduce accidental access to unrelated paths.
- Tool calls, approvals, usage, and session metadata are written to local state
  for later inspection.

## Development

The CLI entry point is `cmd/codog`. Most behavior lives under `internal/` in
packages for the agent loop, providers, tools, sessions, configuration, hooks,
skills, plugins, MCP, code intelligence, background work, and the TUI.

Run the standard checks before submitting changes:

```sh
go test ./...
go build ./cmd/codog
```

Keep generated state, secrets, machine-specific paths, and tool-attribution
signatures out of commits.

## License

Codog is released under the [MIT License](LICENSE).
