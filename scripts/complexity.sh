#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'complexity: %s\n' "$*" >&2
  exit 2
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
baseline="${script_dir}/complexity-baseline.txt"
raw="$(mktemp)"
current="$(mktemp)"
trap 'rm -f "${raw}" "${current}"' EXIT

command -v golangci-lint >/dev/null 2>&1 || die "golangci-lint is required"

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

if [ "${1:-}" = "--update" ]; then
  cp "${current}" "${baseline}"
  printf 'complexity: updated baseline with %s findings\n' "$(wc -l <"${current}" | tr -d ' ')"
  exit 0
fi
if [ -n "${1:-}" ]; then
  die "usage: scripts/complexity.sh [--update]"
fi
if [ ! -f "${baseline}" ]; then
  die "missing baseline; run scripts/complexity.sh --update"
fi

awk -F '[|]' '
  NR == FNR {
    if (NF != 4) {
      printf "complexity: malformed baseline line %d\n", FNR > "/dev/stderr"
      invalid = 1
      next
    }
    key = $1 "|" $2 "|" $3
    allowed[key] = $4 + 0
    next
  }
  {
    key = $1 "|" $2 "|" $3
    score = $4 + 0
    if (!(key in allowed)) {
      printf "complexity: new finding %s (score %d)\n", key, score > "/dev/stderr"
      failed = 1
    } else if (score > allowed[key]) {
      printf "complexity: regression %s (%d > %d)\n", key, score, allowed[key] > "/dev/stderr"
      failed = 1
    }
  }
  END {
    if (invalid || failed) {
      exit 1
    }
  }
' "${baseline}" "${current}" || die "complexity gate failed"

awk -F '[|]' '
  {
    count[$1]++
    if ($4 > max[$1]) {
      max[$1] = $4
    }
  }
  END {
    printf "complexity: %d cognitive findings (max %d), %d cyclomatic findings (max %d); no regressions\n",
      count["gocognit"], max["gocognit"], count["gocyclo"], max["gocyclo"]
  }
' "${current}"
