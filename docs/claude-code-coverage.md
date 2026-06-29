# Claude-Code-Class Coverage

Codog does not copy or translate leaked Claude Code source. This document tracks
clean-room capability coverage by behavior.

## Available

- Prompt and REPL agent loop.
- Anthropic-compatible streaming client.
- Core tools: shell, read, write, edit, grep, glob.
- Permission modes and interactive approval.
- JSONL sessions and resume.
- Config from files, environment, and flags.
- Bubble Tea prompt composer.
- Slash command registry.
- Markdown skills discovery and selected skill injection.
- Pre/post tool hooks.
- Cost and token approximation.
- Auto-compaction.
- Mock Anthropic-compatible harness.
- Background command registry with logs.
- Local agent definition inventory.
- Local plugin manifest inventory.
- PKCE generation for OAuth flows.
- Sandbox strategy detection.
- Go symbol scanning.
- Notebook cell editing.
- Machine-readable capability contract.

## Experimental Or Partial

- MCP: stdio server inspection and `tools/list`; full tool invocation lifecycle
  still needs deeper runtime integration.
- Plugin marketplace: local manifests are supported; remote install, trust, and
  signature policy are not complete.
- Code intelligence: Go symbol scanning and notebook editing exist; full LSP
  process orchestration is not complete.
- Sandbox: strategy detection exists; command execution is not yet isolated.
- OAuth: PKCE helper exists; browser/device login and secure token storage are
  not complete.

## Planned

- IDE bridge.
- Remote sessions with resumable transport.
- Enterprise managed policy and audit pipeline.
- Updater and binary provenance flow.
- Full multi-agent worker orchestration with isolated worktrees.

Use `codog capabilities --json` for the current machine-readable status.
