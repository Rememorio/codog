# Codog Roadmap

Codog is split into three delivery horizons. The intent is to keep the main
branch honest: shipped capabilities are available in the CLI, and longer-term
surfaces expose explicit status instead of pretending to be complete.

## Horizon 1: MVP

Target: 4-8 weeks.

Available now:

- Single Go binary.
- One-shot prompt and REPL.
- Anthropic-compatible streaming.
- Core tools for shell, file read/write/edit, grep, and glob.
- Permission confirmation.
- JSONL session persistence and resume.
- Basic config from user file, project file, environment, and flags.

## Horizon 2: Practical

Target: 2-4 months.

Available now as first-pass surfaces:

- Bubble Tea prompt composer with `codog tui`.
- Slash commands for status, cost, compaction, skills, and MCP inspection.
- Markdown skill discovery.
- Hook commands around tool use.
- Token and rough cost estimation.
- Request-context auto-compaction.
- Deterministic mock Anthropic-compatible streaming server.

Remaining depth:

- Rich full-screen TUI.
- End-to-end MCP tool invocation.
- Stronger skill packaging.
- More accurate tokenizer and provider pricing.
- Broader parity harness scenarios.

## Horizon 3: Claude-Code-Class

Target: 6-12 months or more.

Planned surfaces are visible through:

```bash
codog roadmap
codog bridge
codog remote
codog agents
codog enterprise
codog marketplace
codog sandbox
codog updater
```

These commands report current status and next steps for:

- IDE bridge.
- Remote sessions.
- Multi-agent orchestration.
- Background tasks.
- Notebook and LSP tools.
- OAuth.
- Enterprise policy.
- Plugin marketplace.
- Cross-platform sandboxing.
- Updater and release provenance.

For automation, use:

```bash
codog capabilities --json
```
