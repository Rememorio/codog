# Codog Architecture

Codog follows a small-core layout:

- `cmd/codog`: binary entrypoint.
- `internal/agent`: CLI, REPL, prompt loop, slash commands, and model/tool
  orchestration.
- `internal/anthropic`: Anthropic-compatible streaming client and event parser.
- `internal/tools`: built-in tool registry and permission gate.
- `internal/session`: JSONL session persistence.
- `internal/config`: config merge from user file, project file, environment, and
  flags.
- `internal/hooks`: pre/post tool hook runner.
- `internal/skills`: Markdown skill discovery.
- `internal/mcp`: minimal stdio MCP lifecycle inspection.
- `internal/tui`: Bubble Tea prompt composer.
- `internal/usage`: approximate token and cost accounting.
- `internal/future`: explicit long-horizon capability status.

The project deliberately keeps model-provider contracts and tool behavior
separate from UI surfaces. That lets the one-shot CLI, REPL, TUI, future IDE
bridge, and remote session transport share the same runtime loop.
