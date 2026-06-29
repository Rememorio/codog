# Codog Architecture

Codog follows a small-core layout:

- `cmd/codog`: binary entrypoint.
- `internal/agent`: CLI, REPL, prompt loop, slash commands, and model/tool
  orchestration.
- `internal/runloop`: provider-agnostic model/tool turn loop shared by CLI,
  REPL, TUI, and future transports.
- `internal/anthropic`: Anthropic-compatible streaming client and event parser.
- `internal/tools`: built-in tool registry and permission gate.
- `internal/session`: JSONL session persistence.
- `internal/config`: config merge from user file, project file, environment, and
  flags.
- `internal/hooks`: pre/post tool hook runner.
- `internal/slash`: slash command registry and help rendering.
- `internal/harness`: in-process mock-provider smoke harness.
- `internal/skills`: Markdown skill discovery.
- `internal/mcp`: stdio MCP discovery plus tool/resource calls.
- `internal/tui`: Bubble Tea prompt composer.
- `internal/usage`: approximate token and cost accounting.
- `internal/future`: explicit long-horizon capability status.
- `internal/background`: background process metadata, log registry, and watch
  event streaming.
- `internal/agentdefs`: local agent definition discovery.
- `internal/worktree`: git worktree allocation for isolated local agent workers.
- `internal/plugins`: local plugin discovery, remote marketplace index fetch,
  verified zip install/update, enable/disable, and removal.
- `internal/oauth`: PKCE and local auth token storage.
- `internal/sandbox`: local sandbox strategy detection and bash wrapping.
- `internal/codeintel`: lightweight Go symbols, diagnostics, and notebook
  helpers.
- `internal/control`: HTTP control API for sessions, background tasks, logs,
  watch streams, diagnostics, remote auth, structured failure state, and
  heartbeat leases.
- `internal/bridge`: stdio JSON-RPC bridge with workspace, diagnostics, files,
  sessions, and background watch notifications.
- `internal/updater`: release manifest check, signed manifest verification,
  verified artifact downloads, and backup-based binary installation.
- `internal/signing`: shared Ed25519 public-key and signature decoding for
  signed manifests and managed policy files.

The project deliberately keeps model-provider contracts and tool behavior
separate from UI surfaces. That lets the one-shot CLI, REPL, TUI, bridge, and
remote session transport share the same runtime loop.
