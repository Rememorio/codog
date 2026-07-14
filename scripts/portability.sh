#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'portability: %s\n' "$*" >&2
  exit 2
}

step() {
  printf '\n==> %s\n' "$*"
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"

command -v go >/dev/null 2>&1 || die "go is required"
command -v actionlint >/dev/null 2>&1 || die "actionlint is required"
command -v shellcheck >/dev/null 2>&1 || die "shellcheck is required"

cd "${repo_root}"

step "GitHub Actions workflows"
actionlint

step "shell scripts"
shellcheck scripts/*.sh

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

targets=(linux/amd64 linux/arm64 darwin/arm64 windows/amd64)
for target in "${targets[@]}"; do
  goos="${target%/*}"
  goarch="${target#*/}"
  output="${tmp_dir}/codog-${goos}-${goarch}"
  if [ "${goos}" = "windows" ]; then
    output="${output}.exe"
  fi
  step "build ${goos}/${goarch}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" go build -trimpath -o "${output}" .
done
