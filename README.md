# Codog

Codog is a Go-native coding agent CLI for working inside a repository.

It is an independent implementation inspired by Claude Code-style local coding
workflows: a terminal agent, a permissioned tool loop, resumable local sessions,
and extension points that are easy to inspect because they live in ordinary
files.

Codog is not affiliated with Anthropic.

> Codog is pre-1.0. The core local workflow is usable, but command names,
> configuration fields, compatibility behavior, and extension surfaces can still
> change.

## Why Codog

Most coding agents are useful because of the loop around the model, not just the
model call itself. Codog focuses on making that loop small, local, and auditable:

- one Go binary for the normal terminal workflow;
- JSONL sessions that can be resumed, exported, compacted, and inspected;
- read, write, edit, grep, glob, shell, git, todo, notebook, and MCP-backed
  tools behind workspace scope and permission checks;
- project context from `AGENTS.md`, Codog config, memory, focused files, skills,
  hooks, slash commands, and prompt templates;
- extension surfaces for local commands, plugins, MCP servers, hooks, skills,
  editor bridges, background work, and mock parity testing.

The goal is not to hide complexity behind a hosted product. The goal is to make a
serious coding-agent runtime that can be understood, modified, and shipped as Go.

## Current Fit

Codog is a good fit today if you want to experiment with a local coding-agent
runtime, inspect how the agent loop is implemented, or build Go-native tooling
around repository automation.

It is still evolving if you need a polished enterprise deployment story, a large
plugin marketplace, deep IDE collaboration, remote multi-user sessions, or exact
behavioral parity with every Claude Code surface.

## Quick Start

Codog requires Go 1.24 or newer and an Anthropic-compatible API key.

```bash
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY="sk-ant-..."
codog -p "summarize this repository"
```

Run `codog repl` for an ongoing terminal session. Use `codog --help`,
`codog help <topic>`, or `codog capabilities --json` when you need the complete
local command and tool surface.

## How It Works

Codog treats the current directory as the active workspace. Each turn follows the
same basic shape:

1. assemble repository instructions, configuration, memory, focused files,
   session history, skills, slash commands, and available tools;
2. stream an Anthropic-compatible model response;
3. route requested tool calls through workspace scope, permission mode,
   allow/deny rules, and hooks;
4. persist the conversation and tool results to a local JSONL session.

That design keeps the important state close to the repository and makes agent
behavior easier to debug than a mostly remote workflow.

## Local Files

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
| `.codog/plugins` | local plugin manifests and extension content |

Compatible `.claude` instruction, command, and skill locations are loaded where
the Go implementation supports them.

## Extension Model

Codog's extension points are intentionally file-based first:

- slash commands are Markdown files with optional frontmatter;
- skills are Markdown instruction bundles that can be invoked directly or loaded
  by the agent;
- hooks run local commands around lifecycle, tool, notification, and session
  events;
- plugins package commands, skills, hooks, tools, agents, and MCP servers behind
  a manifest;
- MCP can be used both as a client surface and as a way to expose Codog tools to
  other local agents.

These pieces are meant to be small enough to review before enabling them.

## Configuration

Configuration is layered from user defaults, project config, local overrides,
environment variables, and CLI flags. The most common project-level settings are
the model, permission mode, maximum turns, token budget, additional workspace
directories, MCP servers, hooks, and allow/deny rules.

Secrets should stay in environment variables or local config. Shared repository
config should describe behavior, not developer machines.

## Development

The source tree is organized around a small CLI entry point and focused internal
packages:

- `cmd/codog` starts the CLI;
- `internal/agent` dispatches commands and top-level workflows;
- `internal/runloop` drives model turns and tool execution;
- `internal/anthropic` contains the Anthropic-compatible client and protocol
  types;
- `internal/tools` implements workspace tools, permissions, aliases, and MCP
  integration;
- `internal/session` stores JSONL sessions;
- `internal/config` loads layered configuration;
- `internal/tui`, `internal/mcp`, `internal/hooks`, `internal/plugins`, and
  `internal/bridge` provide optional integration surfaces.

Build and test from a checkout with:

```bash
go test ./...
go build ./cmd/codog
```

Keep generated state, machine-specific paths, local cache locations, secrets,
and tool-generated attribution out of code, docs, commits, and examples.

## License

Codog is released under the [MIT License](LICENSE).
