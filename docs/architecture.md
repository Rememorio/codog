# Codog Architecture

Codog follows a small-core layout:

- `cmd/codog`: binary entrypoint.
- `internal/agent`: CLI, REPL, prompt loop, slash commands, and model/tool
  orchestration.
- `internal/runloop`: provider-agnostic model/tool turn loop shared by CLI,
  REPL, TUI, and remote transports.
- `internal/anthropic`: Anthropic-compatible streaming client and event parser.
  The client owns provider retry/backoff behavior for transport, 429, and
  selected 5xx failures.
- `internal/tools`: built-in tool registry and permission gate.
- `internal/webaccess`: bounded HTTP fetch and HTML search-result extraction
  used by model-facing web tools.
- `internal/pathscope`: workspace-local additional directory state and prompt
  rendering for multi-root file tool access.
- `internal/session`: workspace-scoped JSONL session persistence, export
  rendering, and legacy flat-session compatibility.
- `internal/prompthistory`: prompt-history report shaping and text rendering for
  CLI and REPL slash surfaces.
- `internal/config`: config merge from user file, project file, environment, and
  flags, including provider rate-limit retry settings.
- `internal/contextview`: prompt-context report shaping and rendering for
  memory, focus, session, token, git, and local preflight signals.
- `internal/planmode`: workspace-local read-only planning mode state,
  system-prompt injection, and command report rendering.
- `internal/projectinit`: idempotent project bootstrap for `.codog`
  instructions, shared config, and local ignore entries.
- `internal/memory`: project instruction discovery, system prompt injection,
  and memory report rendering.
- `internal/focus`: focused context path persistence, report rendering, and
  system prompt injection for selected files/directories.
- `internal/promptrefs`: scoped `@path` prompt reference expansion before model
  turns.
- `internal/todos`: workspace todo persistence shared by CLI, slash commands,
  and model-facing todo tools.
- `internal/securityreview`: local heuristic scanner for credential and
  shell-command risk patterns.
- `internal/review`: changed-file review summaries that combine git diff
  metadata with security findings for touched paths.
- `internal/workerstate`: file-based worker observability surface shared by
  REPL, one-shot prompts, and `codog state`.
- `internal/versioninfo`: version, build metadata, runtime, and workspace
  provenance reporting.
- `internal/hooks`: pre/post tool hook runner.
- `internal/slash`: slash command registry and help rendering.
- `internal/harness`: in-process mock-provider smoke harness.
- `internal/skills`: Markdown skill discovery.
- `internal/customcommands`: user, workspace, and Claude-compatible Markdown
  custom command discovery and argument rendering.
- `internal/templates`: user/workspace Markdown prompt template discovery and
  variable rendering.
- `internal/outputstyle`: built-in, user, and workspace output style discovery,
  active-style persistence, reporting, and system prompt injection.
- `internal/mcp`: stdio MCP discovery plus tool/resource calls.
- `internal/tui`: Bubble Tea prompt composer.
- `internal/usage`: approximate token/cost accounting plus session usage
  breakdowns by role, content block, and tool call.
- `internal/background`: background process metadata, session attachment, log
  registry, restart policy supervision, retention pruning, and watch event
  streaming.
- `internal/agentdefs`: local agent definition discovery.
- `internal/worktree`: git worktree allocation for isolated local agent workers.
- `internal/gitops`: local git status, diff, and commit helpers for CLI and
  slash-command workflows.
- `internal/releasenotes`: structured git-log parsing and Markdown/JSON release
  note generation.
- `internal/plugins`: local plugin discovery, remote marketplace index fetch,
  verified zip install/update, enable/disable, and removal.
- `internal/oauth`: PKCE, provider metadata discovery/profile storage, device
  and browser authorization, refresh-token renewal, token revocation/logout,
  auth status inspection, and keychain-backed auth token storage with file fallback.
- `internal/sandbox`: local sandbox strategy detection and bash wrapping.
- `internal/codeintel`: lightweight Go symbols, diagnostics, notebook helpers,
  and local LSP lifecycle metadata.
- `internal/control`: HTTP control API for sessions, session-scoped background
  tasks, terminal command transport, logs, restart/prune/supervise operations,
  watch streams, diagnostics, remote auth, structured failure state, and
  heartbeat leases.
- `internal/bridge`: stdio JSON-RPC bridge with trusted editor identity,
  open-file/selection state, workspace info, file list/search/diff/read/write/edit
  actions, diagnostics, sessions, and background watch notifications.
- `internal/updater`: release manifest check, signed manifest verification,
  verified artifact downloads, and backup-based binary installation.
- `internal/signing`: shared Ed25519 public-key and signature decoding for
  signed manifests and managed policy files.

The project deliberately keeps model-provider contracts and tool behavior
separate from UI surfaces. That lets the one-shot CLI, REPL, TUI, bridge, and
remote session transport share the same runtime loop.
