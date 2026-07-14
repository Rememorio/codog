#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'quality: %s\n' "$*" >&2
  exit 2
}

step() {
  printf '\n==> %s\n' "$*"
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"

command -v go >/dev/null 2>&1 || die "go is required"
command -v golangci-lint >/dev/null 2>&1 || die "golangci-lint is required"

cd "${repo_root}"

step "golangci-lint config"
golangci-lint config verify

step "source formatting"
format_diff="$(golangci-lint fmt --diff)"
if [ -n "${format_diff}" ]; then
  printf '%s\n' "${format_diff}"
  die "Go source files are not formatted"
fi

step "module files"
go mod tidy -diff

step "static analysis"
golangci-lint run

step "complexity gate"
scripts/complexity.sh
