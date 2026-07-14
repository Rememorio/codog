#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'complexity: %s\n' "$*" >&2
  exit 2
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
raw="$(mktemp)"
current="$(mktemp)"
trap 'rm -f "${raw}" "${current}"' EXIT

command -v golangci-lint >/dev/null 2>&1 || die "golangci-lint is required"
if [ -n "${1:-}" ]; then
  die "usage: scripts/complexity.sh"
fi

cd "${repo_root}"
golangci-lint config verify --config .golangci-complexity.yml

status=0
golangci-lint run --config .golangci-complexity.yml ./... >"${raw}" 2>&1 || status=$?
if [ "${status}" -gt 1 ]; then
  cat "${raw}" >&2
  die "golangci-lint failed with status ${status}"
fi

# The parser expression is intentionally literal; shell expansion would corrupt it.
# shellcheck disable=SC2016
sed -En 's#^([^:]+):[0-9]+:[0-9]+: (cognitive|cyclomatic) complexity ([0-9]+) of func `([^`]+)`.* \((gocognit|gocyclo)\)$#\5|\1|\4|\3#p' "${raw}" \
  | LC_ALL=C sort >"${current}"

if [ "${status}" -ne 0 ] && [ ! -s "${current}" ]; then
  cat "${raw}" >&2
  die "could not parse complexity findings"
fi

if [ -s "${current}" ]; then
  cat "${raw}" >&2
  die "expected zero findings"
fi

printf 'complexity: 0 cognitive findings, 0 cyclomatic findings\n'
