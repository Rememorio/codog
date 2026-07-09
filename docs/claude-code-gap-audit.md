# Claude Code Gap Audit

This audit compares Codog's current Go implementation with the local Claude
Code reference source used during development. It is a product-shape audit, not
a source-code copy plan. Codog should preserve the local coding-agent workflow
while staying honest about what is verified, experimental, or intentionally out
of scope.

## Reference Surface

The reference checkout is represented by these local surfaces:

- `main.tsx`: CLI startup, configuration, auth, command dispatch, and REPL/TUI
  launch.
- `query.ts`: streaming model loop, tool-use orchestration, compaction, token
  budget checks, and recovery paths.
- `tools.ts`: base tools, permissions, MCP tools, task tools, notebook and LSP
  helpers, and feature-gated tools.
- `commands.ts`: slash command registry and command filtering.
- `replLauncher.tsx`, `ink.ts`: Ink-based interactive shell.
- `history.ts`, `context.ts`, `cost-tracker.ts`, `costHook.ts`: session
  history, context, and usage reporting.

Codog's matching surfaces are `internal/agent`, `internal/runloop`,
`internal/tools`, `internal/tui`, `internal/session`, `internal/config`,
`internal/mcp`, `internal/skills`, `internal/hooks`, and
`internal/control`.

## Machine Surface Audit

The command and tool name audit is green:

```sh
codog capabilities audit --reference-root PATH --json
```

Current result:

- Commands: 96 reference commands, 96 covered, 0 missing.
- Tools: 84 reference tools, 84 covered, 0 missing.

This proves that Codog exposes compatible command and tool names for the local
reference snapshot. It does not prove equal product behavior, equal UI polish,
or availability of hosted Anthropic services.

`codog capabilities --json` currently reports:

- `terminal.status`: `ready`
- `orchestration.status`: `ready`
- `bridge.status`: `degraded`
- `release.status`: `degraded`

The degraded bridge and release states are expected for local closure because
official IDE services, hosted remote sessions, managed policy rollout, and
official updater channels are not part of this repository's completion target.

## Closed Local Core

These capabilities are closed for the current local-use target because they
have unit or integration coverage, real binary coverage where user-visible, and
manual live-provider smoke where a live model is required.

| Capability | Status | Evidence |
| --- | --- | --- |
| Default interactive entrypoint | Closed | Bare `codog --model glm52` opens full-screen TUI and completes a real TTY question. |
| Explicit TUI entrypoint | Closed | `codog --model glm52 tui` opens full-screen TUI and completes a real TTY question. |
| One-shot prompt | Closed | `codog --model glm52 -p ... --max-turns 1` completes a live OpenAI-compatible request. |
| Legacy REPL | Closed | `codog repl` remains line-oriented, supports local slash help/exit, and is documented as legacy mode. |
| TUI send and multiline keys | Closed | Enter sends; Alt+Enter and Ctrl+J insert newlines; Ctrl+S is no longer required. |
| TUI slash commands | Closed | Real TTY acceptance covers `/help`, `/status`, and `/exit`. |
| TUI error details | Closed | Real TTY acceptance covers provider error body fallback and actionable hints. |
| TUI tool results | Closed | Real TTY acceptance covers model-requested `write_file` and `bash` tool summaries inside the TUI transcript. |
| Workspace tools | Closed | Real binary run-loop acceptance covers `bash`, `read_file`, `write_file`, `edit_file`, `grep`, and `glob`. |
| Permission confirmation | Closed | Real binary acceptance covers approve and deny paths in `workspace-write` mode. |
| Session JSONL resume | Closed | Real binary acceptance verifies resumed requests include prior user and assistant history. |
| OpenAI-compatible `glm52` routing | Closed | Unit coverage verifies routing and wire model behavior; live smoke verifies configured runtime use. |
| Provider error fallback | Closed | Unit and real binary acceptance verify empty provider bodies include actionable hints instead of only `400 Bad Request`. |
| Mock parity | Closed as regression gate | `codog mock-parity --json` passes 90 of 90 scenarios. |

## Partially Aligned Local Surfaces

These are useful and implemented, but should still be treated as experimental
or workflow-specific until they have deeper live usage across repositories.

| Surface | Current state | Practical next action |
| --- | --- | --- |
| Advanced command palette behavior | Slash commands exist and basic TUI discovery works, but the interaction is simpler than Ink command palettes. | Add focused TTY coverage for history navigation, fuzzy command selection, and long command output scrolling only if users miss those paths. |
| TUI streaming detail | Assistant text streams through the run loop; TUI records final turn output and tool summaries. | If needed, render incremental assistant/tool events in-place rather than waiting for the turn to finish. |
| MCP client/server | Config loading, lifecycle, calls, resources, prompts, auth diagnostics, and mock parity paths exist. | Validate against real third-party MCP servers before claiming production interoperability. |
| Skills and custom commands | Discovery, invocation, and command rendering are implemented. | Add repository-level examples only after real projects use them. |
| Hooks | Hook lifecycle and permission/tool hooks are implemented. | Keep hook behavior covered by mock parity and add real shell hook smoke when a project depends on it. |
| Cost and token tracking | Usage and estimated cost reporting exist. | Keep provider-specific pricing and token accounting explicit; avoid claiming billing-grade accuracy. |
| Auto-compaction | Message-threshold compaction is covered by mock parity. | Exercise on long live sessions before treating it as mature. |
| Background tasks and local agents | Task, agent, team, cron, and worker surfaces exist. | Treat as experimental orchestration until failure recovery is dogfooded on real work. |
| Notebook and LSP helpers | Go syntax fallback and LSP routes exist. | Validate per language server and repository before promoting as stable. |
| Remote control and bridge APIs | Local control APIs and bridge routes exist, but capability status is degraded without full external service setup. | Keep local APIs documented as experimental; do not imply official IDE parity. |

## Explicit Non-Goals

These gaps should not be chased as part of this local Go-agent closure target:

- Official Anthropic account, subscription, quota, and hosted OAuth behavior.
- Official Claude Code IDE extensions or cloud remote sessions.
- Enterprise policy distribution, organization administration, or managed
  rollout.
- Official plugin marketplace distribution, signing, and hosted trust flows.
- Official updater channels.
- A hostile-repository sandbox guarantee across every platform.
- Source-level compatibility with Claude Code internals.

## Current Closure Checklist

The local closure target is satisfied when the following commands pass on a
machine with a configured `glm52` OpenAI-compatible provider:

```sh
go test ./...
codog mock-parity --json
codog --model glm52 -p "print a short arithmetic answer" --max-turns 1
codog --model glm52
codog --model glm52 tui
codog --model glm52 repl
```

For the interactive commands, use a real TTY or an `expect` script and verify
that an assistant answer is rendered after submission. For TUI tool visibility,
the repository acceptance test `TestRealBinaryTUIShowsToolResultsWithTTY`
exercises a real binary, a real TTY, model-requested tools, visible tool
summaries, and the resulting workspace write.
