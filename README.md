# Codog

Codog is an experimental Go-native coding agent that runs from your terminal.
It keeps the important parts of an agent runtime in one inspectable binary:
model streaming, workspace tools, permissions, local sessions, hooks, skills,
MCP, and interactive shells.

Codog is inspired by Claude Code, but it is not an Anthropic product and does
not try to present itself as one. The goal is to build a practical local agent
while keeping the implementation understandable enough to study, modify, and
extend.

Codog is most useful today when you want:

- a terminal coding agent with local, file-backed state;
- a Go codebase that shows how agent loops and tool execution fit together;
- explicit permission checks before model-requested actions affect a workspace;
- room to experiment with Claude-Code-style workflows without a multi-runtime
  stack.

It is not yet a polished drop-in replacement for commercial coding agents.

## Quick Start

Codog requires Go 1.26 or newer and at least one model credential.

```sh
go install github.com/Rememorio/codog/cmd/codog@latest
export ANTHROPIC_API_KEY=<key>
```

From a source checkout, install the single binary into `~/.local/bin`:

```sh
scripts/install.sh
```

Use `scripts/install.sh --bin-dir DIR` to install into a specific directory.

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

Codog is usable for local experimentation and ordinary repository workflows,
but the surface area is intentionally labeled by reliability.

Stable enough for daily local use:

- one-shot prompts, REPL, text output, JSON output, and streaming JSON output;
- Anthropic-compatible streaming, plus configurable routes for
  OpenAI-compatible APIs, Ollama, xAI, DashScope, and custom base URLs;
- local shell, read, write, edit, grep, glob, git, and notebook tools;
- JSONL sessions with resume, search, history, rewind, summaries, exports,
  usage reporting, compaction, hygiene audits, and oversized-log rotation;
- permission modes, allow and deny rules, workspace boundary checks, hooks,
  audit events, and setup diagnostics;
- `codog doctor`, `codog status`, `codog onboarding`, and `codog mock-parity`
  as repeatable health and compatibility checks.

Useful but still experimental:

- Bubble Tea TUI, slash commands, skills, templates, output styles, and
  Markdown agent definitions;
- MCP client and server paths, including lifecycle diagnostics and auth
  recovery reporting;
- code intelligence and LSP routing for symbols, diagnostics, hover,
  references, completion, formatting, code actions, and notebook cells;
- background tasks, task boards, team runs, subagents, branch freshness checks,
  and remote-control surfaces;
- OAuth helpers, provider profiles, updater manifests, policy reports,
  marketplace metadata, desktop/mobile handoff manifests, and bridge commands.

Not production-hardened yet:

- hosted remote sessions and remote identity management;
- organization-wide policy rollout and enterprise administration;
- plugin marketplace trust, signing, distribution, and update channels;
- cross-platform sandbox enforcement for hostile repositories or commands;
- IDE extensions, notebook/LSP integrations, and multi-agent orchestration at
  the level expected from a commercial tool.

The CLI exposes many compatibility commands because they are useful for testing
and migration work. A command being present does not mean the surrounding
product workflow is finished.

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

## Code Intelligence

Codog includes an experimental Go-focused code-intelligence layer behind the
`lsp` tool and the `/code-intel` slash command. Without a configured language
server it can answer common Go repository queries from syntax trees and `go`
tool diagnostics, including symbols, definitions, references, hover text,
completion candidates, diagnostics, formatting previews, and rename previews.

When a configured LSP server is available, Codog can route supported requests to
that server for richer actions such as semantic tokens, code lenses, inlay
hints, inline values, code actions, call hierarchy, and type hierarchy. These
routes are useful for integration testing and local workflows, but they should
be treated as experimental until they have been exercised against the specific
server and repository you plan to use.

## Configuration

Codog separates shared project defaults from personal settings and local
secrets.

| Location | Purpose |
| --- | --- |
| `AGENTS.md`, `CLAUDE.md`, `CLAW.md` | Project instructions loaded into the agent context |
| `AGENTS.local.md`, `CLAUDE.local.md`, `CLAW.local.md` | Local instruction overrides |
| `.claude/CLAUDE.md`, `.claw/CLAUDE.md`, `.claw/instructions.md`, `.codog/instructions.md` | Tool-scoped project instructions |
| `.codog.json` | Shared project configuration |
| `.codog.local.json` | Uncommitted local overrides |
| `.codog/commands` | Project slash commands |
| `.codog/skills` | Project skills |
| `.codog/hooks` | Project hook scripts |

## Repository Scope

Start Codog from the smallest useful package or service directory when possible.
For monorepos, this usually means `cd services/api` instead of starting at the
repository root, then using `codog add-dir ../shared` only for sibling code that
the task actually needs. This keeps avoidable files out of context before they
burn prompt tokens.

Codog reports first-run scope guidance through `codog onboarding`. The report
calls out heavy/generated paths such as `node_modules`, `dist`, `build`,
`.next`, `coverage`, `logs`, `dumps`, `generated`, and `reports`, and explains
the active ignore files it found.

Use `codog files --output-format json` for a lightweight workspace weight
preview before a broad prompt. The `scope_risk` section distinguishes a clean
workspace from trees likely to burn tokens quickly, lists concrete token sinks,
and recommends narrower scope choices or ignore/cleanup targets.

Use `codog scope preview` to turn those findings into actionable choices.
`codog scope apply` can switch the current runtime workspace to a safer source
subdirectory or append a reversible `.codogignore` block; `codog scope restore`
returns to the broader workspace. `codog scope status` reports whether a
safer-scope state is currently active.

Ignore-file behavior:

- `.gitignore` is honored by `grep` and `glob` when `respectGitignore` is
  enabled, which is the default, and by `ls` directory listings.
- `.codogignore`, `.claudeignore`, and `.clawignore` are honored by `ls`
  directory listings for local pruning.

Use project ignore files to exclude generated artifacts, dependency caches,
coverage output, logs, dumps, and generated reports from routine discovery.

Common provider variables:

- `CODOG_CONFIG_HOME`, `CLAUDE_CONFIG_HOME`, or `CLAUDE_CONFIG_DIR`
- `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, or `CLAUDE_CODE_OAUTH_TOKEN`
- `CODOG_MODEL`, `ANTHROPIC_MODEL`, `ANTHROPIC_DEFAULT_MODEL`, or `CLAUDE_MODEL`
- `CODOG_ADVISOR_MODEL` or `ANTHROPIC_SMALL_FAST_MODEL`
- `CODOG_MAX_TOKENS` or `ANTHROPIC_MAX_TOKENS`
- `CODOG_TEMPERATURE` or `ANTHROPIC_TEMPERATURE`
- `CODOG_REASONING_EFFORT` or `ANTHROPIC_REASONING_EFFORT`
- `ANTHROPIC_BASE_URL` or `CODOG_BASE_URL`
- `OPENAI_API_KEY` and `OPENAI_BASE_URL`
- `OLLAMA_HOST`
- `XAI_API_KEY`
- `DASHSCOPE_API_KEY`

Keep credentials in environment variables or local-only config. Do not commit
API keys, generated sessions, caches, private prompts, or machine-specific
paths.

MCP servers can be declared with Codog's `mcp_servers` key, Claude-style
`mcpServers`, or VS Code-style `mcp.servers`; all three are normalized into the
same runtime server list.

Use `codog config help --output-format json` to inspect the section names
supported by the current binary. The main public sections are:

| Section | Examples |
| --- | --- |
| `auth` | API key, auth token, OAuth profile, base URL |
| `model` | model, advisor model, token and turn limits, temperature |
| `permissions` | permission mode, allow and deny rules |
| `sandbox` | strategy and sandbox runtime options |
| `remote` | remote control enablement, auth token, lease duration |
| `editor_bridge` | local IDE bridge socket and token |
| `background` | worker state file path |
| `preferences` | Chrome default, notifications, ultra review preference |
| `compatibility` | counters and URLs for Claude-Code-compatible commands |
| `marketplace` | plugin marketplace sources and public keys |
| `enterprise` | managed policy file and verification key |
| `updater` | release manifest URL for update checks and downloads |

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
scripts/smoke.sh
```

For narrower checks, run `go test ./...`, `go vet ./...`,
`go build ./cmd/codog`, or `scripts/install.sh --bin-dir ./bin` directly.

Keep changes portable. Avoid committing generated caches, API keys,
machine-specific setup snippets, local absolute paths, or tool attribution text.

## Mock Parity

`codog mock-parity` runs deterministic compatibility scenarios without calling a
live model provider. A mock provider emits assistant text and tool-use blocks,
then Codog executes its real run loop, tools, permissions, hooks, sessions,
MCP paths, and reporting surfaces.

Use it when changing core runtime behavior:

```sh
codog mock-parity
codog mock-parity manifest --output-format json
```

The command is not a claim that Codog is identical to Claude Code. It is a
repeatable contract for the Claude-Code-style workflows this implementation
currently supports.

## License

Codog is released under the [MIT License](LICENSE).
