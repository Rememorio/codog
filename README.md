# Codog

Codog is a Go-native coding agent CLI. The project is intentionally clean-room:
it uses public API contracts and its own tests rather than translating leaked
Claude Code source.

## Features

- Single Go binary: `codog`
- One-shot prompt mode
- Interactive REPL
- Anthropic-compatible streaming through `/v1/messages`
- Built-in tools: `bash`, `read_file`, `write_file`, `edit_file`, `grep`, `glob`
- Permission confirmation with `read-only`, `workspace-write`,
  `danger-full-access`, `prompt`, and `allow` modes
- JSONL session persistence and resume
- Basic config from `~/.codog/config.json`, `.codog.json`, environment, and flags

- `codog tui` starts a Bubble Tea prompt composer.
- REPL slash commands: `/help`, `/status`, `/cost`, `/compact`, `/skills`, `/mcp`.
- `codog skills` lists Markdown skills from `~/.codog/skills` and
  `.codog/skills`.
- `codog mcp` inspects configured stdio MCP servers, and configured MCP tools
  are exposed to the model as `mcp__server__tool` tool calls.
- Hook commands can run before and after tool use.
- `codog cost --resume latest` estimates session token usage and rough cost.
- Request context is automatically compacted for long sessions.
- `codog mock-server :8089` starts a deterministic Anthropic-compatible
  streaming server for harness tests.
- `codog self-test` runs the prompt loop against an in-process mock provider.
- `enabled_skills` injects selected Markdown skills into the system prompt.
- `codog capabilities --json` exposes the long-horizon capability contract.
- `codog background run|list|status|stop|logs` manages local background
  commands and their logs.
- `codog agents` lists `.codog/agents/*.json` agent definitions.
- `codog marketplace` lists local `.codog/plugins/*/plugin.json` manifests.
- `codog oauth pkce` generates a PKCE verifier/challenge pair.
- `codog sandbox` reports detected strategies; `future.sandbox_strategy` can
  wrap `bash` tool execution with `detect`, `sandbox-exec`, `bwrap`, or
  `unshare`.
- `codog code-intel symbols` scans Go symbols, and `notebook-edit` updates
  `.ipynb` cells.
- `codog remote serve [addr]` starts a local HTTP session-control API.
- `codog bridge serve` starts a stdio JSON-RPC bridge for sessions,
  workspace info, and bounded file read/write/edit operations.
- `codog updater check URL` checks a release manifest.
- `codog enterprise audit [limit]` prints recent local permission and tool-use
  audit events.

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

`~/.codog/config.json` or `.codog.json`:

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
    "enterprise_policy": "",
    "sandbox_strategy": "detect",
    "editor_bridge_socket": "",
    "updater_manifest_url": "",
    "plugin_marketplaces": []
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
