# Codog

Codog is a Go-native coding agent for local repositories.

It provides a single-binary CLI for asking a model to inspect code, edit files,
run local tools, and resume prior work with explicit permission boundaries.
The project is compatibility-oriented: Codog aims to match the observable
developer workflow of tools such as Claude Code without copying private
internals or requiring a large runtime stack.

> Status: pre-1.0. Codog is usable for experiments and local automation, but it
> is not yet a drop-in replacement for mature commercial coding agents. Command
> details, configuration fields, and compatibility behavior may change.

## Highlights

- **Single binary:** install and run Codog as a normal Go CLI.
- **Local-first:** sessions, approvals, audit records, and configuration stay
  visible on disk.
- **Permission-aware:** tool execution can be read-only, prompt-gated,
  workspace-scoped, or explicitly unrestricted.
- **Agent workflow:** use one-shot prompts, a REPL, or a Bubble Tea terminal UI.
- **Compatibility surface:** slash commands, skills, hooks, MCP, plugins, and
  JSONL sessions are designed around observable behavior.

## Quick Start

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY=<key>
codog -p "summarize this repository"
```

For interactive work, run:

```sh
codog repl
```

For setup and feature discovery:

| Need | Command |
| --- | --- |
| Check provider and config health | `codog doctor` |
| See the current compatibility surface | `codog capabilities` |
| Read the full CLI reference | `codog --help` |

## Working Model

Codog treats an agent session as local, reviewable development work.

| Concept | How it works |
| --- | --- |
| Providers | Anthropic Messages streaming and OpenAI-compatible chat endpoints are supported through config and environment variables. |
| Tools | File reads, writes, edits, patches, search, shell commands, git helpers, notebooks, and code-intelligence tools are exposed through permission checks. |
| Sessions | Conversations are stored as JSONL records with resume, history, summaries, export, usage, token, and cost metadata. |
| Extensions | Slash commands, prompt templates, hooks, skills, plugins, and MCP servers extend the agent surface. |
| Safety | Permission mode, allow and deny lists, trusted roots, broad-cwd guards, and audit records keep local actions inspectable. |

For unfamiliar repositories, start read-only:

```sh
codog --permission-mode read-only -p "explain the project structure"
```

## Feature Surface

The current implementation covers the core loop and many compatibility edges,
but depth varies by subsystem. Treat `codog capabilities` and the test suite as
the source of truth when exact behavior matters.

| Area | Current shape |
| --- | --- |
| Agent entry points | One-shot prompt, quick side questions, REPL, and TUI. |
| Local tools | Bash, read, write, edit, patch, grep, glob, git, notebook, and code-intelligence workflows. |
| Context | Repository instructions, focused paths, memory files, summaries, compaction, and session replay. |
| Automation | Hooks, background tasks, cron-style prompts, review helpers, and PR drafting helpers. |
| Integrations | MCP client/server pieces, editor bridge state, OAuth profiles, provider profiles, plugin marketplace metadata, and compatibility roots. |
| Operations | Doctor reports, status output, usage and cost summaries, audit records, sandbox detection, and updater workflows. |

## Configuration

Codog layers defaults, environment variables, user configuration, project
configuration, local overrides, and command-line flags. Shared team behavior
belongs in committed project files; secrets and personal preferences belong in
local overrides or environment variables.

| Path | Purpose |
| --- | --- |
| `AGENTS.md` | Repository instructions loaded into working context. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Uncommitted local overrides. |
| `.codog/commands` | Project slash commands. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Project hooks and local automation. |

Compatible instruction, command, and skill locations from related agent tools
are loaded where that behavior has been implemented.

## Project Status

Codog is being developed as a pragmatic Go rewrite of a local coding-agent
workflow. The near-term goal is not to clone private internals; it is to build a
portable, inspectable implementation whose external behavior can be tested.

Current priorities:

- harden compatibility contracts with mock providers and parity harnesses;
- make thin advanced surfaces fail clearly instead of pretending to be complete;
- keep local state portable and auditable;
- preserve a small, understandable Go codebase while the feature surface grows.

## Development

The CLI entry point is `cmd/codog`. Most behavior lives in focused packages
under `internal/`, including the agent loop, providers, tools, sessions,
configuration, slash commands, hooks, skills, plugins, MCP, code intelligence,
and the TUI.

Run the standard checks before submitting changes:

```sh
go test ./...
go build ./cmd/codog
```

Keep generated state, secrets, machine-specific paths, and tool-attribution
signatures out of commits.

## License

Codog is released under the [MIT License](LICENSE).
