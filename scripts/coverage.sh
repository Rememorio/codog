#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'coverage: %s\n' "$*" >&2
  exit 2
}

step() {
  printf '\n==> %s\n' "$*"
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
base="${CODOG_COVERAGE_BASE:-}"
threshold="${CODOG_COVERAGE_THRESHOLD:-85}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --base)
      shift
      [ "$#" -gt 0 ] || die "--base requires a Git revision"
      base="$1"
      ;;
    --base=*)
      base="${1#--base=}"
      ;;
    --threshold)
      shift
      [ "$#" -gt 0 ] || die "--threshold requires a percentage"
      threshold="$1"
      ;;
    --threshold=*)
      threshold="${1#--threshold=}"
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
  shift
done

command -v go >/dev/null 2>&1 || die "go is required"
command -v git >/dev/null 2>&1 || die "git is required"

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

cd "${repo_root}"
profile="${tmp_dir}/coverage.out"
diff_file="${tmp_dir}/changes.diff"

step "coverage tests"
go test ./... -covermode=atomic -coverprofile="${profile}"

step "overall coverage"
go tool cover -func="${profile}" | tail -n 1

case "${base}" in
  ""|0000000000000000000000000000000000000000)
    printf '\nNo base revision supplied; changed-line coverage was not evaluated.\n'
    exit 0
    ;;
esac

git cat-file -e "${base}^{commit}" 2>/dev/null || die "base revision is unavailable: ${base}"
git diff --unified=0 --find-renames --diff-filter=AMR "${base}...HEAD" -- '*.go' ':!*_test.go' >"${diff_file}"

step "changed-line coverage"
go run ./internal/coveragecheck/cmd \
  --profile "${profile}" \
  --diff "${diff_file}" \
  --module "$(go list -m)" \
  --threshold "${threshold}"
