# Capability Contract

`codog capabilities --json` is the machine-readable contract for long-horizon
surfaces. It is intentionally honest: each surface reports a status and next
steps instead of exposing placeholder behavior as if it were production-ready.

Current status values:

- `available`: implemented for normal use.
- `experimental`: callable shape exists, but behavior is not complete.
- `planned`: roadmap surface only.

Implemented local foundations:

- `background run|list`: background process metadata and logs.
- `agents`: local agent definition inventory.
- `marketplace`: local plugin manifest inventory.
- `oauth pkce`: PKCE helper.
- `sandbox`: OS sandbox strategy detection.
- `code-intel symbols`: lightweight Go symbol scanner.
- `code-intel notebook-edit`: basic notebook cell editing.

Example:

```bash
codog capabilities --json
codog remote --json
```

Long-horizon surfaces currently tracked:

- `bridge`
- `remote`
- `agents`
- `background`
- `code-intel`
- `oauth`
- `enterprise`
- `marketplace`
- `sandbox`
- `updater`
