# Codog

Codog is a Go-native coding agent CLI. The project is intentionally clean-room:
it uses public API contracts and its own tests rather than translating leaked
Claude Code source.

## MVP surface

- Single Go binary: `codog`
- One-shot prompt mode
- Interactive REPL
- Anthropic-compatible streaming through `/v1/messages`
- Built-in tools: `bash`, `read_file`, `write_file`, `edit_file`, `grep`, `glob`
- Permission confirmation with `read-only`, `workspace-write`,
  `danger-full-access`, `prompt`, and `allow` modes
- JSONL session persistence and resume
- Basic config from `~/.codog/config.json`, `.codog.json`, environment, and flags

## Practical surface

Codog also includes the first practical-layer surfaces:

- `codog tui` starts a Bubble Tea prompt composer.
- REPL slash commands: `/help`, `/status`, `/cost`, `/compact`, `/skills`, `/mcp`.
- `codog skills` lists Markdown skills from `~/.codog/skills` and
  `.codog/skills`.
- `codog mcp` inspects configured stdio MCP servers and attempts `tools/list`.
- Hook commands can run before and after tool use.
- `codog cost --resume latest` estimates session token usage and rough cost.
- Request context is automatically compacted for long sessions.
- `codog mock-server :8089` starts a deterministic Anthropic-compatible
  streaming server for harness tests.

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
  "max_turns": 8,
  "max_tokens": 4096,
  "hooks": {
    "pre_tool_use": ["echo pre >&2"],
    "post_tool_use": ["echo post >&2"]
  },
  "mcp_servers": {
    "example": {
      "command": "example-mcp-server",
      "args": []
    }
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

## Roadmap

The repository is organized around three delivery horizons:

1. MVP: single binary, core agent loop, core tools, permissions, sessions, config.
2. Practical: TUI, slash commands, MCP, skills, hooks, cost tracking,
   auto-compaction, and a mock parity harness.
3. Long-term: IDE bridge, remote sessions, multi-agent orchestration,
   background tasks, notebook/LSP, OAuth, enterprise policy, plugin marketplace,
   sandboxing, and updater surfaces.

See [docs/roadmap.md](docs/roadmap.md) and `codog roadmap` for the current
capability matrix.
