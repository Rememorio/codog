#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/smoke.sh [options]

Runs the local release smoke gate used by CI:
  - go test ./...
  - go vet ./...
  - go build ./cmd/codog
  - source install smoke
  - mock parity report generation

Options:
  --artifact-dir DIR   Write smoke artifacts to DIR. Defaults to a temp dir.
  --keep-artifacts     Print and keep the temp artifact directory.
  -h, --help           Show this help text.

Environment:
  CODOG_SMOKE_ARTIFACT_DIR   Default artifact directory.
  CODOG_SMOKE_KEEP_ARTIFACTS Set to 1 to keep temp artifacts.
EOF
}

die() {
  printf 'smoke: %s\n' "$*" >&2
  exit 2
}

step() {
  printf '\n==> %s\n' "$*"
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
artifact_dir="${CODOG_SMOKE_ARTIFACT_DIR:-}"
keep_artifacts="${CODOG_SMOKE_KEEP_ARTIFACTS:-0}"
artifact_dir_is_temp="0"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --artifact-dir)
      shift
      [ "$#" -gt 0 ] || die "--artifact-dir requires a directory"
      artifact_dir="$1"
      ;;
    --artifact-dir=*)
      artifact_dir="${1#--artifact-dir=}"
      ;;
    --keep-artifacts)
      keep_artifacts="1"
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
  shift
done

command -v go >/dev/null 2>&1 || die "go is required"

if [ -z "${artifact_dir}" ]; then
  artifact_dir="$(mktemp -d)"
  artifact_dir_is_temp="1"
fi
mkdir -p "${artifact_dir}"
artifact_dir="$(cd -- "${artifact_dir}" && pwd)"

cleanup() {
  if [ "${artifact_dir_is_temp}" = "1" ] && [ "${keep_artifacts}" != "1" ]; then
    rm -rf "${artifact_dir}"
  fi
}
trap cleanup EXIT

cd "${repo_root}"

step "go test"
go test ./...

step "go vet"
go vet ./...

step "go build"
go build ./cmd/codog

install_dir="${artifact_dir}/bin"
step "install smoke"
scripts/install.sh --bin-dir "${install_dir}"
"${install_dir}/codog" --version --json >"${artifact_dir}/version.json"
test -s "${artifact_dir}/version.json" || die "version smoke did not write a report"

step "mock parity"
MOCK_PARITY_REPORT_PATH="${artifact_dir}/mock-parity-report.json" \
  go run ./cmd/codog mock-parity --output-format json >"${artifact_dir}/mock-parity-stdout.json"
test -s "${artifact_dir}/mock-parity-report.json" || die "mock parity report was not written"
test -s "${artifact_dir}/mock-parity-stdout.json" || die "mock parity stdout report was not written"

if [ "${keep_artifacts}" = "1" ] || [ "${artifact_dir_is_temp}" != "1" ]; then
  printf '\nArtifacts written to %s\n' "${artifact_dir}"
fi
