# Codog Agent Guide

This file applies to the entire repository. It defines the engineering
constraints for agents and maintainers changing Codog.

## Product Boundary

Codog is a Go-native, single-binary coding agent for local terminal workflows.
Preserve the local-first design: core execution, permissions, sessions, tools,
extensions, and interactive behavior must remain inspectable and usable without
a hosted Codog service.

Claude Code may be used as a behavioral reference for local interaction and
workflow design. Do not copy proprietary implementation details, present Codog
as an Anthropic product, or claim support for commercial account, subscription,
enterprise backend, official IDE application, or hosted remote-session features
that this repository cannot provide.

## Before Changing Code

- Read `README.md` for public behavior and `RELEASING.md` for release work.
- Inspect the relevant package, its tests, and callers before choosing a design.
- Check the worktree first and preserve changes that are outside the task.
- Use `rg` and `rg --files` for repository discovery.
- Prefer the existing package boundaries and data formats over a parallel
  implementation.
- Reproduce bugs at the narrowest useful layer before editing when practical.

## Architecture

- Keep `main.go` thin. Command parsing, runtime wiring, and agent-loop behavior
  belong in `internal/agent`; domain behavior belongs in focused packages.
- Keep provider protocol details in provider packages. The run loop should work
  with normalized messages, streaming events, usage, and tool calls.
- Route every model-requested host action through the tool registry, permission
  checks, path scope, hooks, policy, and sandbox decisions that apply to it.
- Keep sessions, configuration, reports, manifests, and bridge protocols
  structured. Use typed encoding instead of constructing JSON, YAML, or JSONL
  with string concatenation.
- Keep the TUI driven by state and messages. Long-running work must not block
  Bubble Tea updates, and background goroutines must not write directly into an
  active terminal UI.
- Add abstractions only when they remove real duplication or enforce a shared
  contract. Small, direct code is preferred for local behavior.

## Compatibility Contracts

Treat these surfaces as compatibility-sensitive:

- command names, flags, aliases, exit codes, stdout and stderr separation;
- JSON output fields, schema identifiers, and machine-readable error shapes;
- session JSONL records, resume behavior, identity metadata, and compaction;
- configuration precedence, accepted compatibility keys, and environment
  variables;
- tool names, argument schemas, permission behavior, hook events, MCP messages,
  and bridge protocols;
- TUI key behavior, completed-turn scrollback, streaming state, interruption,
  permission prompts, and terminal restoration.

Do not silently reinterpret an existing field or reuse a schema identifier for
an incompatible shape. Prefer additive changes and explicit migration logic.
One-shot and piped commands must never wait for an invisible TTY prompt. In the
TUI, Enter submits, Ctrl+J inserts a newline, and interruption must cancel the
active operation without corrupting the session or terminal.

## Security And Trust

- Treat model output, repository content, hooks, MCP responses, and tool
  arguments as untrusted input.
- Enforce permission and workspace boundaries before host side effects, not
  after a command has started.
- Normalize and validate paths, including symlink and additional-directory
  behavior, before reading or writing.
- Preserve explicit confirmation for destructive, privileged, remote, or
  out-of-scope operations.
- Never log or persist credentials, authorization headers, private keys, or
  secret-bearing configuration values.
- Do not weaken safety checks to make a test or compatibility scenario pass.

## Go Style

- Target the Go version declared in `go.mod` and use current standard-library
  APIs and language built-ins where they simplify the code.
- Prefer clear ownership, small interfaces at consumer boundaries, and concrete
  types elsewhere.
- Pass `context.Context` through cancellable network, process, worker, and agent
  operations. Clean up processes, goroutines, files, and terminal state on every
  exit path.
- Wrap errors with operation context while preserving causes needed by
  `errors.Is` and `errors.As`. Do not panic for user, provider, or repository
  input failures.
- Keep package documentation on every package and Go doc comments on exported
  APIs. Comments should explain contracts or non-obvious decisions.
- Avoid mutable global state, unnecessary dependencies, speculative
  generalization, and unrelated refactors.

## Testing And Validation

- Add focused unit tests beside changed behavior. Use `testify` where it makes
  assertions clearer and follow the style already used by the package.
- Add regression tests for bug fixes and contract tests for structured output.
- Exercise cancellation, concurrency, timeout, partial-stream, malformed-input,
  and cleanup paths when the change can affect them.
- Use real-binary acceptance tests for CLI, REPL, TUI, permission, session, and
  installation behavior. UI assertions should cover terminal state, not only
  internal model values.
- Keep mock parity deterministic, but do not treat it as a substitute for a
  real provider test when provider transport or streaming behavior changes.
- Run targeted tests while iterating. Before delivery, run `scripts/smoke.sh`.
- When changing concurrency-sensitive code, run the relevant packages with the
  race detector. When changing the command entry point or release packaging,
  also verify root-module installation and `scripts/release.sh` output.

## Documentation And Repository Hygiene

- Keep `README.md` focused on users and public behavior. Keep release operations
  in `RELEASING.md` and code contracts in Go documentation.
- Update examples and help text in the same change as user-visible behavior.
- Do not add status journals, generated audit dumps, local caches, built
  binaries, credentials, or machine-specific paths to the repository.
- Do not add assistant attribution, generator signatures, or tool branding to
  code, comments, documentation, commits, or release notes.
- Keep terminology and compatibility claims factual and independently
  verifiable.

## Delivery

- Keep commits cohesive and match the repository's conventional commit style.
- Stage only files that belong to the task. Do not rewrite unrelated history or
  discard another contributor's work.
- A change is done only when the user-facing path works end to end, relevant
  tests pass, structured contracts remain valid, documentation matches behavior,
  and the worktree contains no accidental artifacts.
- Releases must follow `RELEASING.md`. Never move a published tag; fix release
  defects with a new version.
