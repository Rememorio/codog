# Codog

Codog is a Go-native coding agent for terminal-based repository work. It aims
to keep the agent runtime small, local, and inspectable: one binary, streamed
Anthropic-compatible model calls, permissioned workspace tools, JSONL sessions,
and file-based project configuration.

Codog is inspired by Claude Code-style workflows, but it is an independent
implementation. It is not affiliated with Anthropic and is not a drop-in
replacement for Claude Code.

## Why Codog

- **Single Go binary:** easy to build, ship, inspect, and run locally.
- **Local state:** sessions are stored as JSONL and can be resumed, exported,
  diffed, or debugged with ordinary tools.
- **Permissioned tools:** file, shell, search, git, and extension tools pass
  through workspace scope and confirmation checks.
- **Compatibility-oriented:** Claude-style commands, project files, aliases,
  and protocol surfaces are added where they make the Go runtime more useful.

## Project Status

Codog is pre-1.0. The core local workflow is usable, while compatibility
surfaces, command names, and extension points are still expected to change.

It is useful today for:

- one-shot repository prompts and interactive REPL sessions;
- streamed Anthropic-compatible model calls;
- permissioned read, write, edit, grep, glob, bash, git, todo, notebook, web,
  and code-intel tools;
- resumable local sessions, project instructions, memory, focused files, slash
  commands, skills, hooks, MCP surfaces, and mock parity harnesses.

Wait before relying on Codog for exact Claude Code parity, enterprise policy,
remote multi-user operation, or a mature plugin ecosystem.

## Install

Codog requires Go 1.24 or newer and an Anthropic-compatible API key.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
```

From a checkout:

```bash
go test ./...
go build ./cmd/codog
```

## First Run

Run Codog from the repository you want it to inspect.

```bash
codog -p "summarize this repository"
codog repl
```

Use `codog --help` for the human CLI surface and
`codog capabilities --json` for the machine-readable command, tool, protocol,
and feature inventory.

## How Codog Works

Each turn follows the same local pipeline:

1. load project instructions, layered config, session history, memory, focus,
   slash commands, skills, and available tools;
2. send the assembled request to an Anthropic-compatible model endpoint;
3. route tool calls through path scope, permission mode, allow/deny rules, and
   hooks;
4. append conversation events and tool results to a local JSONL session.

The design goal is to make important state easy to find, diff, resume, and
debug with ordinary repository tools.

## Project Files

Codog reads repository-local files when present:

| Path | Purpose |
| --- | --- |
| `AGENTS.md` | project instructions for the agent |
| `.codog.json` | shared project configuration |
| `.codog.local.json` | uncommitted local overrides |
| `.codog/commands` | Markdown slash commands |
| `.codog/templates` | reusable prompt templates |
| `.codog/skills` | project skills |
| `.codog/hooks` | local automation hooks |
| `.codog/plugins` | plugin manifests and packaged extension content |

Compatible `.claude` instruction, command, and skill locations are loaded where
the Go implementation supports them.

## Development

The CLI entry point is intentionally thin. Most behavior lives in focused
internal packages for the agent loop, Anthropic protocol, tools, sessions,
configuration, MCP, hooks, skills, plugins, TUI, bridges, and worker workflows.

For a quick local check, run:

```bash
go test ./...
```

Keep generated state, machine-specific paths, local cache locations, secrets,
and tool attribution out of code, docs, commits, and examples.

## Roadmap

Codog is being developed in three broad phases:

- **MVP:** single Go binary, one-shot prompt, REPL, Anthropic streaming,
  workspace tools, permission confirmation, JSONL sessions, resume, and basic
  config.
- **Practical daily use:** Bubble Tea TUI, slash commands, MCP client, skills,
  hooks, cost and token tracking, auto-compaction, and mock parity harnesses.
- **Claude Code-class parity:** IDE bridge, remote sessions, multi-agent work,
  background jobs, notebook and LSP surfaces, OAuth, policy controls, plugin
  marketplace, cross-platform sandboxing, and updater support.

## License

Codog is released under the [MIT License](LICENSE).
