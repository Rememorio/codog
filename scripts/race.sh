#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"

command -v go >/dev/null 2>&1 || {
  printf 'race: go is required\n' >&2
  exit 2
}

packages=(
  ./internal/anthropic
  ./internal/background
  ./internal/control
  ./internal/mcp
  ./internal/runloop
  ./internal/tui
  ./internal/workers
  ./internal/workerstate
)

cd "${repo_root}"
go test -race -count=1 "${packages[@]}"
