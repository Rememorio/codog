# Codog

Codog is a Go-native coding agent CLI for repository work. It keeps the core
agent loop local and inspectable: one binary, JSONL sessions, permissioned
workspace tools, resumable conversations, and file-based extension points.

Codog is an independent implementation inspired by Claude Code-style terminal
workflows. It is not affiliated with Anthropic and is not a drop-in replacement
for Claude Code.

## Status

Codog is pre-1.0. The local workflow is usable, but command names,
configuration fields, compatibility shims, and extension surfaces can still
change.

It is a good fit today if you want to:

- experiment with a Go implementation of a coding-agent runtime;
- inspect or modify the model/tool/session loop;
- build local repository automation around permissioned tools;
- prototype Claude Code-compatible workflows without depending on a hosted
  runtime.

It is still maturing if you need exact behavior parity, hardened enterprise
policy controls, a large plugin marketplace, deep IDE collaboration, or remote
multi-user operation.

## Install

Codog requires Go 1.24 or newer and an Anthropic-compatible API key.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
```

From a checkout, build and test with:

```bash
go build ./cmd/codog
go test ./...
```

## First Run

Run a one-shot prompt from the repository you want Codog to inspect:

```bash
codog -p "summarize this repository"
```

Start an interactive terminal session:

```bash
codog repl
```

Use `codog --help` for the human CLI surface and `codog capabilities --json` for
the machine-readable command, tool, protocol, and feature inventory.

## What Codog Provides

Codog is organized around a small set of local runtime concerns instead of a
large remote product surface.

| Area | Current capability |
| --- | --- |
| Agent loop | One-shot prompts, REPL, Bubble Tea TUI, streamed Anthropic-compatible responses |
| Workspace tools | Read, write, edit, grep, glob, shell, git, todo, notebook, web, and code-intel tools |
| Permissions | Workspace scope checks, allow/deny rules, prompt mode, read-only and write modes |
| Sessions | JSONL storage, resume, fork, rename, rewind, export, summary, usage, and compaction |
| Context | `AGENTS.md`, memory, focused files, prompt history, project config, templates, and output style |
| Extensions | Slash commands, skills, hooks, plugins, MCP client/server, local agents, and background tasks |
| Integrations | GitHub PR workflows, editor and remote-control bridges, OAuth profiles, sandbox toggles, and updater plumbing |

## How It Works

Codog treats the current directory as the active workspace. Each turn follows the
same basic path:

1. load repository instructions, layered config, session history, memory, focus,
   skills, slash commands, and available tools;
2. send the assembled request to an Anthropic-compatible model endpoint;
3. route tool calls through path scope, permission mode, allow/deny rules, and
   hooks;
4. append conversation events and tool results to a local JSONL session.

The design goal is to make the important state easy to find, diff, and debug in
ordinary files.

## Project Files

Codog reads these repository-local files when present:

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

## Development Map

The codebase keeps the CLI entry point thin and places behavior in focused
internal packages:

| Package | Role |
| --- | --- |
| `cmd/codog` | CLI entry point |
| `internal/agent` | command dispatch and top-level workflows |
| `internal/runloop` | model turns and tool execution |
| `internal/anthropic` | Anthropic-compatible client and protocol types |
| `internal/tools` | workspace tools, permissions, aliases, and MCP exposure |
| `internal/session` | JSONL session storage and exports |
| `internal/config` | layered configuration loading |
| `internal/tui` | terminal UI |
| `internal/mcp` | MCP client support |
| `internal/hooks` | lifecycle and tool hook execution |
| `internal/plugins` | plugin manifests, marketplace state, and extension loading |
| `internal/bridge` | editor and remote-control bridge support |

Keep generated state, machine-specific paths, local cache locations, secrets,
and tool attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
