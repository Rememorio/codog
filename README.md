# Codog

Codog is a Go-native coding agent CLI. The project is intentionally clean-room:
it uses public API contracts and its own tests rather than translating leaked
Claude Code source.

## Features

- Single Go binary: `codog`
- One-shot prompt mode through `codog prompt` or Claude-compatible `codog -p`
- Interactive REPL with Tab completion for built-in and custom slash commands,
  skill shortcuts, model switches, and recent sessions on real terminals
- Anthropic-compatible streaming through `/v1/messages`, plus explicit
  OpenAI-compatible Chat Completions streaming when the model starts with
  `openai/`
- Built-in tools: `bash`, `bash_output`, `kill_bash`, `powershell`, `read_file`, `write_file`, `edit_file`,
  `multi_edit`, `grep`, `glob`, `ls`, `web_fetch`, `web_search`, `remote_trigger`,
  `notebook_read`, `notebook_edit`,
  `lsp`, `agent`, `cron_create`, `cron_delete`, `cron_list`,
  `team_create`, `team_list`, `team_get`, `team_delete`,
  `worker_create`, `worker_list`, `worker_get`, `worker_observe`, `worker_resolve_trust`,
  `worker_await_ready`, `worker_send_prompt`, `worker_restart`,
  `worker_terminate`, `worker_observe_completion`,
  `enter_worktree`, `exit_worktree`,
  `enter_plan_mode`, `exit_plan_mode`, `ask_user_question`,
  `brief`, `send_user_message`, `structured_output`, `sleep`, `repl`, `tool_search`, `skill`, `config`,
  `mcp`, `mcp_auth`, `list_mcp_resources`,
  `read_mcp_resource`, `list_mcp_resource_templates`, `list_mcp_prompts`,
  `get_mcp_prompt`, `task_create`, `run_task_packet`, `task_list`,
  `task_status`, `task_get`, `task_update`, `task_stop`, `task_output`,
  `task_supervise`,
  `todo_read`, `todo_write`, `testing_permission`, `git_status`, `git_diff`, `git_log`,
  `git_show`, `git_blame`
- Claude Code-style model tool names such as `Bash`, `BashTool`, `Read`,
  `FileReadTool`, `Write`, `FileWriteTool`, `Edit`, `FileEditTool`,
  `BashOutput`, `KillBash`, `MultiEdit`, `GrepSearch`, `GlobSearch`, `LS`, `NotebookRead`, `Task`, `TodoWrite`, `WebFetch`, and `ExitPlanMode` are accepted
  as execution aliases while Codog keeps one canonical tool definition per
  capability.
- Permission confirmation with `read-only`, `workspace-write`,
  `danger-full-access`, `prompt`, and `allow` modes; compatibility flags
  `--dangerously-skip-permissions` and `--skip-permissions` select `allow`,
  while `--allowed-tools` and `--disallowed-tools` add per-run tool rules
- `testing_permission` dry-runs the current permission policy for a target tool
  without executing that tool.
- Bash execution includes preflight validation for read-only commands,
  destructive patterns, sed in-place edits, and suspicious path targets
- Model, agent, and workspace-scanning commands refuse to run from the home
  directory or filesystem root unless `--allow-broad-cwd` is passed.
- Shell tools return stdout, stderr, exit code, timeout/interruption status, and
  execution duration.
- `bash` keeps a session-scoped current working directory. A command that ends
  in a different directory updates later shell and background task launches and
  can trigger `cwd_changed` hooks.
- `bash` can run in the background with `run_in_background`; `bash_output` and
  `kill_bash` read or stop those background bash tasks through Claude-compatible
  `BashOutput` and `KillBash` aliases.
- `remote_trigger` validates HTTP/HTTPS URLs, supports request timeouts, and
  returns bounded webhook responses with truncation metadata.
- `write_file`, `edit_file`, and `multi_edit` record workspace-local undo
  snapshots; `codog undo` and `/undo` restore the most recent file change.
- JSONL session persistence and resume
- Workspace-scoped session storage with legacy flat-session compatibility
- Basic config from `~/.codog/config.json`, `.codog.json`, environment, and flags
- System prompt override and append support with `--system-prompt` and
  `--append-system-prompt`
- `codog btw QUESTION` and `/btw QUESTION` answer a quick side question in a
  forked session so the active conversation is not modified.

- `codog tui` starts a Bubble Tea prompt composer with Tab completion for
  built-in/custom slash commands, skill shortcuts, common subcommands, the
  current model, and recent sessions.
- REPL slash commands: `/help`, `/status`, `/statusline`, `/config`, `/model`,
  `/advisor`, `/max-tokens`, `/max-turns`, `/permissions`, `/allowed-tools`, `/history`,
  `/todos`, `/clear`, `/resume`, `/rename`, `/rewind`, `/share`, `/version`, `/btw`, `/sandbox`, `/sandbox-toggle`, `/heapdump`, `/project`, `/env`, `/init-verifiers`, `/files`, `/search`,
  `/security-review`, `/bughunter`, `/review`, `/ultrareview`, `/feedback`, `/pr`, `/commit-push-pr`, `/pr-comments`, `/pr_comments`, `/install-github-app`, `/install-slack-app`, `/stickers`, `/passes`, `/issue`, `/context`, `/ctx_viz`, `/focus`, `/unfocus`, `/add-dir`, `/theme`, `/color`, `/vim`, `/effort`, `/fast`, `/voice`, `/chrome`, `/privacy-settings`, `/keybindings`, `/cost`, `/usage`, `/insights`, `/think-back`, `/thinkback`, `/thinkback-play`, `/extra-usage`, `/rate-limit-options`, `/reset-limits`, `/plan`, `/ultraplan`, `/exit-plan`, `/tokens`, `/compact`, `/undo`, `/system-prompt`, `/tool-details`, `/debug-tool-call`,
  `/run`, `/node`, `/python`, `/test`, `/build`, `/lint`, `/symbols`, `/diagnostics`, `/map`,
  `/references`, `/definition`, `/hover`, `/teleport`, `/completion`,
  `/format`, `/branch`, `/tag`, `/release-notes`, `/templates`, `/commands`, `/output-style`, `/skills`, `/hooks`, `/mcp`, `/acp`, `/brief`, `/terminal-setup`, `/terminalSetup`, `/remote-env`, `/remote-setup`, `/web-setup`, `/remote-control`, `/bridge`, `/bridge-kick`, `/desktop`, `/mobile`, `/ide`, `/agents`, `/team`, `/tasks`, `/bashes`, `/background`, `/cron`, `/plugin`, `/plugins`, `/marketplace`, `/providers`, `/login`, `/oauth-refresh`, `/logout`, `/copy`, `/stats`, `/backfill-sessions`.
- `/session`, `/rename`, `codog rename`, and `codog sessions` manage saved
  sessions with list, show, exists, fork, switch, rename, and delete actions.
- `codog backfill-sessions` and `/backfill-sessions` persist prompt history
  records for older sessions that only have user transcript messages.
- `/export` and `codog export` write session transcripts as markdown, JSON,
  raw JSONL, or HTML.
- `/share` and `codog share` write a local share artifact for the current or
  selected session under `.codog/share` by default.
- `/copy` and `codog copy [last|N|all]` copy the latest assistant response, the
  Nth-latest assistant response, or a formatted session transcript to the
  system clipboard.
- `/history` and `codog history [--session ID] [--limit N] [--json]` show
  prompts recorded for a session, with legacy fallback to user transcript
  messages.
- `/summary` and `codog summary [--session ID|--resume latest]` report session
  message counts, token estimates, tool-use counts, and first/last prompt
  previews without exporting the full transcript.
- `/rewind` and `codog rewind [N] [--session ID|--resume latest]` remove recent
  JSONL session messages and trim trailing input records so the next prompt
  resumes from the rewound context.
- `/todos` and `codog todos list|add|start|done|pending|clear` maintain the
  workspace todo state used by the built-in `todo_read` and `todo_write` model
  tools.
- `/files` and `codog files [PATH] [--glob GLOB] [--limit N]` list workspace
  file inventory with size, extension, depth, truncation, and JSON output.
- `/search` and `codog search PATTERN [--path PATH] [--glob GLOB]` search
  workspace file contents without making a provider request.
- `/security-review` and `codog security-review [--limit N]` run a local
  heuristic scan for common credential and shell-command risks.
- `/bughunter` and `codog bughunter [PATH] [--limit N]` scan local code for
  likely correctness issues such as unchecked returns, panics, os.Exit,
  defer-in-loop, and loop variable capture.
- `/review`, `/ultrareview`, `codog review`, and `codog ultrareview`
  summarize changed files, added/deleted lines, and security findings limited
  to the changed paths.
- `/feedback` and `codog feedback` write a local Markdown feedback report with
  version, git, session, model, permission, and workspace diagnostics.
- `/pr`, `/issue`, `codog pr`, and `codog issue` write local Markdown drafts
  from git status, diff stats, recent commits, optional session context, and
  user-provided context.
- `/commit-push-pr` and `codog commit-push-pr MESSAGE` stage changes by
  default, commit, push the branch, and create or update a GitHub PR through
  `gh`; `--dry-run`, `--staged`, `--no-pr`, `--draft`, `--branch`, `--base`,
  and `--remote` keep the workflow explicit.
- `codog node CODE|FILE`, `codog python CODE|FILE`, `/node`, and `/python`
  run JavaScript or Python snippets and scripts through the same timeout and
  text/JSON command runner used by `/run`.
- `/context` and `codog context [--resume latest]` summarize prompt context,
  project memory, focused paths, session token estimates, git state, and local
  preflight signals before a provider request. `codog ctx_viz` writes the same
  preflight data as a local HTML context visualization.
- `/plan`, `/ultraplan`, `/exit-plan`, `codog plan`, and `codog ultraplan`
  manage workspace-local read-only planning mode; active plans are injected
  into the system prompt and force model tool permissions to read-only for
  prompt and REPL turns.
- `/focus` and `codog focus [PATH...]` maintain focused context paths in
  `.codog/focus.json` and inject focused file contents into future prompts;
  `/unfocus` and `codog unfocus [PATH...|--all]` remove them.
- Prompt text can reference files as `@path`; Codog appends scoped file
  contents for paths inside the workspace or configured additional directories.
- `/add-dir` and `codog add-dir [PATH...|remove PATH|clear]` persist
  workspace-local extra directories that `read_file`, `write_file`,
  `edit_file`, `grep`, and `glob` can access after path-scope validation.
- `/diff`, `/commit`, `/branch`, `/tag`, `/log`, `/changelog`,
  `/release-notes`, `/blame`, `/stash`, `/git`, `codog diff`, `codog commit`,
  `codog log`, `codog blame`, and `codog git` provide local git status, diff,
  branch, branch freshness, tag, log, changelog, blame, stash, and commit workflows.
- `codog release-notes [FROM [TO]] [--format markdown|json]` generates grouped
  release notes from git commits, defaulting to the latest tag through `HEAD`
  when a tag exists.
- `/run`, `/test`, `/build`, `/lint`, and matching CLI commands run workspace
  commands with captured stdout/stderr and text or JSON reports.
- `codog skills list|show|invoke|install|uninstall` discovers and manages Markdown skills from
  `~/.codog/skills`, `.codog/skills`, and `.claude/skills`, including
  directory skills with `SKILL.md`; skill frontmatter such as `description`,
  `allowed-tools`, `argument-hint`, `arguments`, `paths`, `when_to_use`,
  `model`, `context`, `agent`, and `effort` is parsed into prompt metadata.
  Skills with `paths` are automatically added to the current system prompt when
  the prompt references matching `@path` files or the workspace focus list
  contains matching paths.
  During direct skill invocation, `allowed-tools` is converted into temporary
  per-turn permission allow rules, including Claude-style entries such as
  `Bash(go test:*)` and aliases such as `Read`.
  `user-invocable: false` hides skills from direct slash/bare invocation, and
  `disable-model-invocation: true` prevents enabled skills from entering the
  system prompt.
  Prompt turns can also invoke a discovered skill by starting input with the skill name.
- `codog templates list|show|apply` finds Markdown prompt templates from
  `~/.codog/templates` and `.codog/templates`, then renders `{{name}}`
  variables for reusable prompts.
- `codog commands list|show|run` and `/commands` discover custom Markdown
  slash commands from `~/.codog/commands`, `.codog/commands`, and
  `.claude/commands`, including nested commands like `team/review.md` as
  `team:review`; command frontmatter such as `description`, `allowed-tools`,
  `argument-hint`, and `arguments` is parsed for list/run metadata, then the
  Markdown body renders `$ARGUMENTS` and `{{args}}`. In the REPL, custom
  commands can also run directly as `/name args` or `/team/review args`, where
  command `allowed-tools` applies as temporary per-turn allow rules.
- `codog output-style list|show|set|clear` discovers built-in, user, and
  workspace Markdown output styles, persists the active workspace style, and
  injects it into future prompts.
- `codog model`, `codog advisor`, `codog max-tokens`, `codog max-turns`,
  `codog permissions`, and `codog allowed-tools` expose the matching runtime
  slash controls as scriptable CLI commands.
- `/theme`, `/color`, `/vim`, `/effort`, `/fast`, `/voice`, `/chrome`, and `/privacy-settings`
  persist local interface, reasoning, runtime, and privacy preferences such as
  terminal theme, vim keybinding mode, reasoning effort, fast mode, external
  voice command enablement, Chrome integration defaults, and prompt history
  recording.
- `/keybindings` and `codog keybindings [show|path|init]` show the active editor
  mode plus REPL, TUI, vim, and slash-command shortcuts, and can create a
  `keybindings.json` template under the Codog config directory.
- `codog mcp list|serve|show|add|remove|tools|auth|call|resources|resource-templates|read|prompts|prompt`
  manages and inspects configured stdio MCP servers, and configured MCP tools
  are exposed to the model as `mcp__server__tool` tool calls or through the
  generic `mcp` dispatcher. MCP server readiness and auth metadata can be
  inspected through `mcp_auth`. Configured MCP resources can be discovered and
  read by the model through `list_mcp_resources` and `read_mcp_resource`; `mcp serve`
  exposes Codog's local tools over stdio MCP.
- `codog capabilities [--json]` reports built-in commands, slash commands,
  model tools, local MCP resources/prompts, protocol support, and feature flags
  for IDE bridges, parity harnesses, and scripts.
- `codog acp`, `codog acp serve`, `codog --acp`, and `/acp` expose ACP/Zed
  integration status. `codog acp serve` starts a stdio JSON-RPC bridge with
  `initialize`, `status`, `session/new`, `prompt`, and `shutdown` methods.
- Hook commands can run on `session_start`, `session_end`, `setup`,
  `user_prompt_submit`,
  `pre_tool_use`, `post_tool_use`, `post_tool_use_failure`,
  `permission_request`, `permission_denied`, `stop`, `stop_failure`,
  `pre_compact`, `post_compact`, `notification`, `subagent_start`,
  `subagent_stop`, `worktree_create`, `worktree_remove`, `cwd_changed`,
  `task_created`, `task_completed`, `instructions_loaded`, and `file_changed`;
  `codog hooks list|run` inspects and test-runs configured hooks with the same
  JSON payload shape used by live sessions. Hook config accepts simple string arrays and the
  documented Claude Code object format with nested command, HTTP, prompt, and
  agent hooks,
  matcher filtering, `if` conditions, per-hook timeouts, shell selection, and
  allow-listed header environment interpolation. Prompt and agent hooks run
  through the configured model with `$ARGUMENTS` expanded to the hook payload.
  Hook commands receive the payload on stdin plus
  `CODOG_HOOK_EVENT`, `CODOG_HOOK_TOOL`, `CODOG_HOOK_INPUT`,
  `CODOG_HOOK_OUTPUT`, `CODOG_HOOK_IS_ERROR`, `CODOG_HOOK_MESSAGE`,
  `CODOG_HOOK_TITLE`, `CODOG_HOOK_NOTIFICATION_TYPE`,
  `CODOG_HOOK_AGENT_ID`, `CODOG_HOOK_AGENT_TYPE`,
  `CODOG_HOOK_AGENT_TRANSCRIPT_PATH`, `CODOG_HOOK_STOP_HOOK_ACTIVE`, and
  `CODOG_HOOK_LAST_ASSISTANT_MESSAGE`, plus permission-specific
  `CODOG_HOOK_TOOL_NAME`, `CODOG_HOOK_TOOL_USE_ID`, and `CODOG_HOOK_REASON`,
  plus worktree-specific `CODOG_HOOK_WORKTREE_ID`,
  `CODOG_HOOK_WORKTREE_PATH`, and `CODOG_HOOK_REF`, plus cwd-specific
  `CODOG_HOOK_OLD_CWD` and `CODOG_HOOK_NEW_CWD`, plus task-specific
  `CODOG_HOOK_TASK_ID`, `CODOG_HOOK_TASK_KIND`, and
  `CODOG_HOOK_TASK_STATUS`, plus instruction-load-specific
  `CODOG_HOOK_MEMORY_TYPE`, `CODOG_HOOK_LOAD_REASON`, `CODOG_HOOK_GLOBS`,
  `CODOG_HOOK_TRIGGER_FILE_PATH`, and `CODOG_HOOK_PARENT_FILE_PATH`, plus
  file-change-specific
  `CODOG_HOOK_FILE_PATH` and `CODOG_HOOK_OPERATION`;
  `setup`, `session_start`, `cwd_changed`, and `file_changed` command hooks also receive
  `CLAUDE_ENV_FILE` and `CODOG_HOOK_ENV_FILE`, where simple `export KEY=value`
  lines are loaded into the current session environment for later shell and
  background task tools;
  run reports include stdout,
  stderr, HTTP status, duration, success, and exit code.
- `codog brief MESSAGE [--status normal|proactive] [--attach PATH]` and
  `/brief` expose the built-in `brief` tool as a human command with optional
  workspace-scoped attachment metadata.
- `codog cost --resume latest` estimates session token usage and rough cost;
  `codog usage --resume latest` and `codog stats --resume latest` add role,
  block, and tool-use breakdowns.
  Both commands use recorded provider token usage when available, including
  cache token fields, and fall back to local estimates for older sessions.
- `codog insights [--limit N]` and `/insights` summarize local sessions,
  prompts, tool usage, and recorded token usage across the workspace.
- `codog think-back [--year YYYY]`, `codog thinkback-play`, `/think-back`,
  and `/thinkback-play` write a local year-in-review HTML report from saved
  sessions, prompts, tool usage, and recorded token usage. Use `--output PATH`
  to choose the destination.
- `codog extra-usage [--admin|--personal] [--no-open]` and `/extra-usage`
  open or print Claude usage settings for managing extra usage, and record a
  local visit count.
- `codog compact --resume latest --keep N` persists a compacted session context
  using the same message compaction logic as long prompt turns.
- `codog rate-limit-options` reports provider retry/backoff settings; Anthropic
  streaming retries transport errors, 429, and selected 5xx responses according
  to `rate_limit`. `codog reset-limits` removes local `rate_limit` overrides.
- Request context is automatically compacted for long sessions.
- `codog mock-server :8089` starts a deterministic Anthropic-compatible
  streaming server for harness tests.
- `codog self-test` runs a multi-scenario prompt/tool/permission parity harness
  against in-process mock providers.
- `codog dump-manifests [--json]` emits the Go resolver inventory for slash
  commands, tools, agents, and skills.
- `codog system-prompt [--json]` renders the final local system prompt without
  making a provider request.
- `enabled_skills` injects selected Markdown skills into the system prompt.
- `codog init [--json]` initializes `.codog/instructions.md`, `.codog.json`,
  and `.gitignore` entries for project-local setup.
- `codog init-verifiers [--dry-run] [--force]` and `/init-verifiers` scan
  top-level project areas and generate verifier skill templates under
  `.claude/skills` or `.codog/skills`.
- Project memory files (`AGENTS.md`, `CLAUDE.md`, `.claude/CLAUDE.md`,
  `CLAW.md`, and `.codog/instructions.md`) are loaded from the git root to the
  workspace and injected into the system prompt.
- `codog memory list|show|add|path|ensure|edit` lists discovered project memory,
  shows or appends notes, resolves memory file paths, creates missing memory
  files, or opens them with `$VISUAL`/`$EDITOR`; `edit --no-open` prepares the
  file without launching an editor.
- `codog project [--json]` reports workspace, git, Go module, Codog directory,
  and memory-file detection metadata.
- `codog env [--json]` reports environment variables inherited by tools with
  sensitive values redacted.
- `codog background run|list|status|stop|restart|logs|watch|prune` manages local
  background commands, attaches them to sessions, supervises restart policies,
  restarts tasks, prunes retained records, and streams status/log events.
  `codog tasks`, `codog bashes`, `/tasks`, and `/bashes` are aliases for the
  same task-management commands.
- `codog cron list|create|delete|due|run-due` and `/cron` manage scheduled
  recurring prompt entries and can start due prompts as background tasks for
  agent-trigger workflows.
- `codog team list|create|get|status|logs|watch|delete` and `/team` manage
  groups of background Codog prompt tasks, including aggregate status, log
  tails, and JSON watch events for multi-agent work.
- `codog agents list|run|worktrees` lists `.codog/agents/*.json` definitions,
  launches named background workers, and can isolate them in git worktrees with
  `agents run --worktree`.
- `codog marketplace list|show|validate|remote|updates|install|install-remote|update|enable|disable|remove`
  manages and validates local plugins, checks marketplace updates, and can
  install or update SHA-256 verified zip bundles from signed remote marketplace
  indexes.
- Enabled plugins can contribute namespaced `commands/`, `skills/`, `agents/`,
  `hooks/hooks.json`, manifest-declared `mcp_servers`, and manifest-declared
  tools from their installed plugin directory.
- `codog reload-plugins` and `/reload-plugins` rebuild the current process
  tool registry from installed local plugins after install, update, enable, or
  disable operations.
- `codog debug-tool-call TOOL JSON` and `/debug-tool-call TOOL JSON` execute a
  registered tool directly through the active permission mode and print text or
  JSON diagnostics with the canonical tool name, permission, duration, output,
  and error state.
- `codog oauth pkce|discover|provider|device|browser` generates PKCE material,
  discovers and stores provider profiles, and runs profile-backed device or
  browser authorization; `oauth status` inspects local auth readiness; `oauth
  logout` revokes and deletes local auth; `oauth token save|show|refresh|revoke|delete`
  manages and refreshes a keychain-backed auth token store with a local file fallback.
- `codog login [browser|device] PROFILE`, `codog oauth-refresh [PROFILE]`, and
  `codog logout [PROFILE]` are top-level aliases for the configured OAuth
  browser/device login, token refresh, and logout flows.
- `codog status [--json]` prints a local workspace/config/session/git/sandbox
  runtime snapshot for humans or scripts.
- `codog statusline [--json]` and `/statusline` print a compact one-line
  workspace, git, model, fast mode, permission, session, and plan summary for
  shell or IDE integrations.
- `codog pr-comments [PR] [--repo OWNER/REPO]` and `/pr-comments` use the
  GitHub CLI to fetch PR-level comments and inline review comments.
- `codog install-github-app [--workflow claude|review|all]` and
  `/install-github-app` create Claude Code GitHub Actions workflow files using
  the official `anthropics/claude-code-action`, with `--dry-run`, custom secret
  names, and `--force` overwrite support.
- `codog install-slack-app [--no-open]` and `/install-slack-app` open or print
  the Claude Slack app Marketplace URL and record a local install-click count.
- `codog stickers [--no-open]` and `/stickers` open or print the Claude Code
  sticker order page and record a local order-click count.
- `codog passes [show|set-url URL|clear-url] [--no-open]` and `/passes`
  manage a local Claude Code guest-pass referral URL. Without a configured
  referral URL, the command opens or prints the official guest-pass docs.
- `codog terminal-setup status|snippet|install|uninstall` and
  `/terminal-setup` inspect or manage idempotent shell integration snippets for
  zsh, bash, fish, and PowerShell.
- `codog remote-env show|set|clear` and `/remote-env` manage default remote
  session enablement, auth-token presence, and lease duration without printing
  token values.
- `codog remote-setup status|enable|disable|clear`, `codog web-setup`, and
  `/remote-setup` or `/web-setup` prepare the local remote-control endpoint,
  report the server command and URLs, and can persist enablement, token
  presence, and lease duration without printing token values.
- `codog desktop`, `codog mobile`, `/desktop`, and `/mobile` report local
  bridge or remote-control handoff instructions for the current or selected
  session, with text and JSON output.
- `codog voice set-command COMMAND` stores an external speech-to-text command;
  `codog voice on` enables voice mode only when that command is executable.
- `codog state [--json]` reads `.codog/worker-state.json`, which REPL and
  one-shot prompt runs update for local observability.
- `codog version [--json]` reports version, Go runtime, target, build metadata,
  executable path, and workspace git provenance.
- `codog doctor [--json]` runs local auth, config, workspace, permission,
  sandbox, git, session, tool registry, and runtime diagnostics without making
  a provider request.
- `codog sandbox` reports detected strategies; `codog sandbox-toggle` and
  `/sandbox-toggle` show or persist `future.sandbox_strategy` for `bash` tool
  execution with `detect`, `off`, `sandbox-exec`, `bwrap`, or `unshare`.
- `codog heapdump [PATH]` and `/heapdump` write a Go heap profile for local
  diagnostics, defaulting to `.codog/heap` when no path is supplied.
- `codog symbols|diagnostics|map|references|definition|hover|teleport|completion|format`
  provides lightweight static Go code intelligence and symbol/file navigation
  without a persistent LSP process. `completion PREFIX` returns static Go
  symbol and keyword candidates, while `format PATH [--write]` previews or
  writes `gofmt` output for workspace-scoped Go files.
- `codog code-intel symbols|diagnostics|completion|format` remains available
  for compatibility, `notebook_read` reads `.ipynb` cell sources and optional
  outputs, `notebook-edit` updates `.ipynb` cells, and
  `lsp discover|start|list|status|stop` manages local language server
  lifecycles. `code-intel lsp query LANGUAGE ACTION PATH [LINE CHARACTER]`
  starts the saved LSP command over stdio for one request when a real server is
  configured.
- `codog remote serve [addr]` starts a local HTTP control API for sessions,
  session message/input mutation and rewind, background prompt turns, background
  tasks, terminal command streams, logs/watch streams, workspace file
  list/search/read/write/edit/diff operations, Go diagnostics and
  code-intelligence queries, editor identity/open-file/selection state,
  bearer-token auth, and heartbeat lease/failure state.
- `codog bridge serve`, `codog remote-control serve`, and
  `/remote-control serve` start a stdio JSON-RPC bridge for trusted editor
  identity, open-file/selection state, session mutation/rewind, background
  prompt turns, workspace info, file listing/search/diff, diagnostics,
  code-intelligence queries, background task control/watch events, and bounded
  file read/write/edit operations.
- `codog bridge-kick [status|clear|poll|error|drop|latency]` and
  `/bridge-kick` inspect, record, or clear local bridge diagnostic events.
- `codog ide [status|clear]` and `/ide` inspect or clear the trusted editor
  bridge state recorded by `codog bridge serve`, including the connected
  editor, active file, and selection.
- `codog updater check|download|install|rollback` checks releases, downloads
  verified artifacts, verifies signed manifests, and installs with a backup
  rollback path.
- `codog upgrade` and `codog install` are top-level aliases for the signed
  updater check/download/install/rollback workflows.
- `codog providers status|list|show|set` inspects the active Anthropic- or
  OpenAI-compatible provider, reports auth readiness without printing secrets,
  lists presets/OAuth profiles, and persists provider base URL/model changes.
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
codog --allow-broad-cwd prompt "inspect my home directory"
```

## Config

Inspect effective config with `codog config`, `codog config get SECTION`, or
`codog config paths`. Persist settings with `codog config set KEY VALUE` and
remove them with `codog config unset KEY`; use `--target project` or
`--target local` to write `.codog.json` or `.codog.local.json`.
Use `codog providers set anthropic`, `codog providers set openai`, or
`codog providers set custom --base-url URL --model MODEL` as focused provider
configuration shortcuts. OpenAI-compatible providers are selected by storing a
model with the `openai/` prefix, for example `openai/gpt-4o-mini`.

`~/.codog/config.json`, `.codog.json`, or `.codog.local.json`:

```json
{
  "model": "claude-sonnet-4-5",
  "permission_mode": "workspace-write",
  "permission_rules": {
    "deny": ["bash:rm -rf"],
    "denied_tools": []
  },
  "rate_limit": {
    "max_retries": 2,
    "initial_backoff_ms": 500,
    "max_backoff_ms": 5000
  },
  "additional_dirs": ["../shared"],
  "max_turns": 8,
  "max_tokens": 4096,
  "enabled_skills": ["go-review"],
  "hooks": {
    "session_start": ["echo session >&2"],
    "session_end": ["echo session-end >&2"],
    "setup": ["echo setup >&2"],
    "user_prompt_submit": ["echo prompt >&2"],
    "pre_tool_use": ["echo pre >&2"],
    "post_tool_use": ["echo post >&2"],
    "post_tool_use_failure": ["echo post-failure >&2"],
    "permission_request": ["echo permission-request >&2"],
    "permission_denied": ["echo permission-denied >&2"],
    "stop": ["echo stop >&2"],
    "stop_failure": ["echo stop-failure >&2"],
    "pre_compact": ["echo compact >&2"],
    "post_compact": ["echo compacted >&2"],
    "notification": ["echo notify >&2"],
    "subagent_start": ["echo agent-start >&2"],
    "subagent_stop": ["echo agent-stop >&2"],
    "worktree_create": ["echo worktree-create >&2"],
    "worktree_remove": ["echo worktree-remove >&2"],
    "cwd_changed": ["echo cwd-changed >&2"],
    "task_created": ["echo task-created >&2"],
    "task_completed": ["echo task-completed >&2"],
    "instructions_loaded": ["echo instructions-loaded >&2"],
    "file_changed": ["echo file-changed >&2"]
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
    "editor_bridge_token": "",
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
- `CODOG_API_KEY`
- `CODOG_AUTH_TOKEN`
- `CODOG_BASE_URL`
- `CODOG_MODEL`
- `OPENAI_API_KEY` when the effective model starts with `openai/`
- `OPENAI_BASE_URL` when the effective model starts with `openai/`
- `CODOG_PERMISSION_MODE`
- `CODOG_CONFIG_HOME`
- `CODOG_RATE_LIMIT_MAX_RETRIES`
- `CODOG_RATE_LIMIT_INITIAL_BACKOFF_MS`
- `CODOG_RATE_LIMIT_MAX_BACKOFF_MS`
- `CODOG_ADDITIONAL_DIRS`
