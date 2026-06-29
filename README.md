# Codog

Codog is a Go-native coding agent CLI. The project is intentionally clean-room:
it uses public API contracts and its own tests rather than translating leaked
Claude Code source.

## Features

- Single Go binary: `codog`
- One-shot prompt mode
- Interactive REPL
- Anthropic-compatible streaming through `/v1/messages`
- Built-in tools: `bash`, `read_file`, `write_file`, `edit_file`, `grep`,
  `glob`, `todo_read`, `todo_write`
- Permission confirmation with `read-only`, `workspace-write`,
  `danger-full-access`, `prompt`, and `allow` modes
- JSONL session persistence and resume
- Workspace-scoped session storage with legacy flat-session compatibility
- Basic config from `~/.codog/config.json`, `.codog.json`, environment, and flags

- `codog tui` starts a Bubble Tea prompt composer.
- REPL slash commands: `/help`, `/status`, `/config`, `/model`,
  `/max-tokens`, `/max-turns`, `/permissions`, `/allowed-tools`, `/history`,
  `/todos`, `/clear`, `/resume`, `/version`, `/sandbox`, `/project`, `/env`, `/search`,
  `/security-review`, `/review`, `/context`, `/focus`, `/unfocus`, `/cost`, `/usage`, `/tokens`, `/compact`, `/system-prompt`, `/tool-details`,
  `/run`, `/test`, `/build`, `/lint`, `/symbols`, `/diagnostics`, `/map`,
  `/references`, `/definition`, `/hover`, `/release-notes`, `/templates`, `/output-style`, `/skills`, `/mcp`.
- `/session` and `codog sessions` manage saved sessions with list, show,
  exists, fork, switch, and delete actions.
- `/export` and `codog export` write session transcripts as markdown, JSON, or
  raw JSONL.
- `/history` and `codog history [--session ID] [--limit N] [--json]` show
  prompts recorded for a session, with legacy fallback to user transcript
  messages.
- `/todos` and `codog todos list|add|start|done|pending|clear` maintain the
  workspace todo state used by the built-in `todo_read` and `todo_write` model
  tools.
- `/search` and `codog search PATTERN [--path PATH] [--glob GLOB]` search
  workspace file contents without making a provider request.
- `/security-review` and `codog security-review [--limit N]` run a local
  heuristic scan for common credential and shell-command risks.
- `/review` and `codog review [--staged|--base REF]` summarize changed files,
  added/deleted lines, and security findings limited to the changed paths.
- `/context` and `codog context [--resume latest]` summarize prompt context,
  project memory, focused paths, session token estimates, git state, and local
  preflight signals before a provider request.
- `/focus` and `codog focus [PATH...]` maintain focused context paths in
  `.codog/focus.json` and inject focused file contents into future prompts;
  `/unfocus` and `codog unfocus [PATH...|--all]` remove them.
- `/diff`, `/commit`, `/log`, `/changelog`, `/release-notes`, `/blame`, `/stash`, `/git`, and
  `codog git` provide local git status, diff, log, changelog, blame, stash, and
  commit workflows.
- `codog release-notes [FROM [TO]] [--format markdown|json]` generates grouped
  release notes from git commits, defaulting to the latest tag through `HEAD`
  when a tag exists.
- `/run`, `/test`, `/build`, `/lint`, and matching CLI commands run workspace
  commands with captured stdout/stderr and text or JSON reports.
- `codog skills` lists Markdown skills from `~/.codog/skills` and
  `.codog/skills`.
- `codog templates list|show|apply` finds Markdown prompt templates from
  `~/.codog/templates` and `.codog/templates`, then renders `{{name}}`
  variables for reusable prompts.
- `codog output-style list|show|set|clear` discovers built-in, user, and
  workspace Markdown output styles, persists the active workspace style, and
  injects it into future prompts.
- `codog mcp` inspects configured stdio MCP servers, and configured MCP tools
  are exposed to the model as `mcp__server__tool` tool calls.
- Hook commands can run before and after tool use.
- `codog cost --resume latest` estimates session token usage and rough cost;
  `codog usage --resume latest` adds role, block, and tool-use breakdowns.
- Request context is automatically compacted for long sessions.
- `codog mock-server :8089` starts a deterministic Anthropic-compatible
  streaming server for harness tests.
- `codog self-test` runs the prompt loop against an in-process mock provider.
- `enabled_skills` injects selected Markdown skills into the system prompt.
- `codog init [--json]` initializes `.codog/instructions.md`, `.codog.json`,
  and `.gitignore` entries for project-local setup.
- Project memory files (`AGENTS.md`, `CLAUDE.md`, `CLAW.md`, and
  `.codog/instructions.md`) are loaded from the git root to the workspace and
  injected into the system prompt.
- `codog memory [--json]` lists discovered project memory files with line,
  character, truncation, and preview metadata without printing full file bodies.
- `codog project [--json]` reports workspace, git, Go module, Codog directory,
  and memory-file detection metadata.
- `codog env [--json]` reports environment variables inherited by tools with
  sensitive values redacted.
- `codog background run|list|status|stop|restart|logs|watch|prune` manages local
  background commands, attaches them to sessions, supervises restart policies,
  restarts tasks, prunes retained records, and streams status/log events.
- `codog agents list|run|worktrees` lists `.codog/agents/*.json` definitions,
  launches named background workers, and can isolate them in git worktrees with
  `agents run --worktree`.
- `codog marketplace list|remote|updates|install|install-remote|update|enable|disable|remove`
  manages local plugins, checks marketplace updates, and can install or update
  SHA-256 verified zip bundles from signed remote marketplace indexes.
- `codog oauth pkce|discover|provider|device|browser` generates PKCE material,
  discovers and stores provider profiles, and runs profile-backed device or
  browser authorization; `oauth status` inspects local auth readiness; `oauth
  logout` revokes and deletes local auth; `oauth token save|show|refresh|revoke|delete`
  manages and refreshes a keychain-backed auth token store with a local file fallback.
- `codog status [--json]` prints a local workspace/config/session/git/sandbox
  runtime snapshot for humans or scripts.
- `codog state [--json]` reads `.codog/worker-state.json`, which REPL and
  one-shot prompt runs update for local observability.
- `codog version [--json]` reports version, Go runtime, target, build metadata,
  executable path, and workspace git provenance.
- `codog doctor [--json]` runs local auth, config, workspace, permission,
  sandbox, git, session, tool registry, and runtime diagnostics without making
  a provider request.
- `codog sandbox` reports detected strategies; `future.sandbox_strategy` can
  wrap `bash` tool execution with `detect`, `sandbox-exec`, `bwrap`, or
  `unshare`.
- `codog symbols|diagnostics|map|references|definition|hover` provides
  lightweight static Go code intelligence without a persistent LSP process.
- `codog code-intel symbols|diagnostics` remains available for compatibility,
  `notebook-edit` updates `.ipynb` cells, and
  `lsp discover|start|list|status|stop` manages local language server
  lifecycles.
- `codog remote serve [addr]` starts a local HTTP control API for sessions,
  background tasks, terminal command streams, logs/watch streams, Go
  diagnostics, bearer-token auth, and heartbeat lease/failure state.
- `codog bridge serve` starts a stdio JSON-RPC bridge for trusted editor
  identity, open-file/selection state, sessions, workspace info, file
  listing/search/diff, diagnostics, background watch events, and bounded file
  read/write/edit operations.
- `codog updater check|download|install|rollback` checks releases, downloads
  verified artifacts, verifies signed manifests, and installs with a backup
  rollback path.
- `codog enterprise audit [limit]` prints recent local permission and tool-use
  audit events, and `enterprise verify POLICY PUBLIC_KEY` verifies signed
  managed policy files.

## Quick start

```bash
go build ./cmd/codog
export ANTHROPIC_API_KEY="sk-ant-..."
./codog prompt "summarize this repository"
./codog repl
```

Useful flags:

```bash
codog --model claude-sonnet-4-5 prompt "write a small plan"
codog --resume latest repl
codog --permission-mode prompt prompt "inspect the repo"
```

## Config

`~/.codog/config.json`, `.codog.json`, or `.codog.local.json`:

```json
{
  "model": "claude-sonnet-4-5",
  "permission_mode": "workspace-write",
  "permission_rules": {
    "deny": ["bash:rm -rf"],
    "denied_tools": []
  },
  "max_turns": 8,
  "max_tokens": 4096,
  "enabled_skills": ["go-review"],
  "hooks": {
    "pre_tool_use": ["echo pre >&2"],
    "post_tool_use": ["echo post >&2"]
  },
  "mcp_servers": {
    "example": {
      "command": "example-mcp-server",
      "args": []
    }
  },
  "future": {
    "remote_auth_token": "",
    "remote_lease_seconds": 0,
    "enterprise_policy": "",
    "enterprise_policy_public_key": "",
    "sandbox_strategy": "detect",
    "editor_bridge_socket": "",
    "updater_manifest_url": "",
    "plugin_marketplaces": [],
    "plugin_marketplace_public_keys": {}
  }
}
```

Environment overrides:

- `ANTHROPIC_API_KEY`
- `ANTHROPIC_AUTH_TOKEN`
- `ANTHROPIC_BASE_URL`
- `CODOG_BASE_URL`
- `CODOG_MODEL`
- `CODOG_PERMISSION_MODE`
- `CODOG_CONFIG_HOME`
