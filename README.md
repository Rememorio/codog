# Codog

Codog is a Go-native coding agent for local repositories.

It is built as a single binary that can read a checkout, stream model output,
ask for permission before risky local actions, and keep session history in
plain JSONL files. The project is compatibility-oriented: Codog aims to match
the observable developer workflow of tools such as Claude Code without copying
private internals or depending on a large runtime stack.

> Status: pre-1.0. Codog is usable for experiments and local automation, but
> command details, configuration fields, and compatibility behavior may still
> change between releases.

## Why Codog

- **One binary.** Install and run it as a normal Go CLI, with no required
  Node.js or Python runtime.
- **Local-first workflow.** Files, shell commands, permissions, sessions, and
  audits stay visible on disk.
- **Compatibility surface.** Slash commands, MCP, hooks, skills, plugins,
  session records, and tool schemas are designed around observable behavior.
- **Testable contracts.** Mock providers and protocol tests make it possible
  to improve parity without relying on hidden implementation details.

## Install

Codog requires Go 1.24 or newer.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
```

Set a model provider credential, then run Codog inside a repository:

```sh
export ANTHROPIC_API_KEY=<key>
codog -p "summarize this repository"
```

Codog also supports Anthropic-compatible and OpenAI-compatible endpoints through
configuration and environment variables. Run `codog doctor` if setup or
provider configuration does not look right.

## First Session

Use a one-shot prompt for bounded questions:

```sh
codog -p "find the main entry point and explain the startup path"
```

Use an interactive session when the task needs several turns:

```sh
codog repl
```

Use the full-screen terminal UI when you want a richer working surface:

```sh
codog tui
```

The exact command and feature surface changes as the implementation grows. For
the current build, prefer:

```sh
codog capabilities
codog --help
```

## What Works Today

Codog currently includes the core pieces needed for a local coding-agent loop:

- one-shot prompts, REPL sessions, and a Bubble Tea TUI
- Anthropic Messages streaming and OpenAI-compatible chat streaming
- permission-gated tools for shell, read, write, edit, patch, grep, glob, git,
  notebook, and code-intelligence workflows
- JSONL sessions with resume, history, summaries, export, usage, token, and
  cost metadata
- layered user/project/local configuration
- slash commands, prompt templates, hooks, skills, plugins, and MCP
- safety controls such as read-only mode, workspace-write mode, allow and deny
  lists, trusted roots, audit records, and sandbox status reporting

Some advanced surfaces are intentionally thin while their contracts are being
hardened. When exact support matters, trust `codog capabilities` and the test
suite over README prose.

## Configuration

Codog layers configuration from defaults, environment variables, user config,
project config, local overrides, and command-line flags. Team behavior belongs
in committed project files; personal preferences and secrets belong in local
overrides or environment variables.

| Path | Purpose |
| --- | --- |
| `AGENTS.md` | Repository instructions loaded into working context. |
| `.codog.json` | Shared project configuration. |
| `.codog.local.json` | Uncommitted local overrides. |
| `.codog/commands` | Project slash commands. |
| `.codog/skills` | Project skills. |
| `.codog/hooks` | Project hooks and local automation. |

Compatible `.claude` instruction, command, and skill locations are loaded where
that compatibility has been implemented.

## Safety Model

Codog treats model-requested tool execution as local work that should be
reviewable and recoverable.

- Permission modes decide whether tools can read, write, run commands, or ask
  before acting.
- Tool allow and deny lists narrow what a session can do.
- Trusted roots and broad-cwd guards reduce accidental access outside the
  intended workspace.
- Audit and session records capture prompts, responses, tool calls, approvals,
  summaries, usage, and estimated cost.

For unfamiliar repositories, start with read-only mode:

```sh
codog --permission-mode read-only -p "explain the project structure"
```

## Development

The CLI entry point is `cmd/codog`. Most behavior lives in focused packages
under `internal/`, including the agent loop, provider clients, tools, sessions,
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
