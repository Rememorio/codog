#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/smoke.sh [options]

Runs the local release smoke gate used by CI:
  - go test ./...
  - go vet ./...
  - go build .
  - source install smoke
  - contract artifact generation

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
build_dir="${artifact_dir}/build"
mkdir -p "${build_dir}"
go build -o "${build_dir}/codog" .

install_dir="${artifact_dir}/bin"
step "install smoke"
scripts/install.sh --bin-dir "${install_dir}"
"${install_dir}/codog" --version --json >"${artifact_dir}/version.json"
test -s "${artifact_dir}/version.json" || die "version smoke did not write a report"

step "mock parity"
MOCK_PARITY_REPORT_PATH="${artifact_dir}/mock-parity-report.json" \
  "${install_dir}/codog" mock-parity --output-format json >"${artifact_dir}/mock-parity-stdout.json"
test -s "${artifact_dir}/mock-parity-report.json" || die "mock parity report was not written"
test -s "${artifact_dir}/mock-parity-stdout.json" || die "mock parity stdout report was not written"
grep -q '"schema_version": "codog.mock_parity.v1"' "${artifact_dir}/mock-parity-report.json" || die "mock parity report schema version is missing"
grep -q '"schema_version": "codog.mock_parity.v1"' "${artifact_dir}/mock-parity-stdout.json" || die "mock parity stdout schema version is missing"

step "contract artifacts"
"${install_dir}/codog" mock-parity manifest --output-format json >"${artifact_dir}/mock-parity-manifest.json"
"${install_dir}/codog" capabilities --output-format json >"${artifact_dir}/capabilities.json"
"${install_dir}/codog" report-schema registry --output-format json >"${artifact_dir}/report-schema-registry.json"
test -s "${artifact_dir}/mock-parity-manifest.json" || die "mock parity manifest was not written"
test -s "${artifact_dir}/capabilities.json" || die "capabilities report was not written"
test -s "${artifact_dir}/report-schema-registry.json" || die "report schema registry was not written"
grep -q '"schema_version": "codog.mock_parity_manifest.v1"' "${artifact_dir}/mock-parity-manifest.json" || die "mock parity manifest schema version is missing"
grep -q '"mock_parity"' "${artifact_dir}/capabilities.json" || die "capabilities report does not expose mock parity metadata"
grep -q '"schema_version": "codog.mock_parity_manifest.v1"' "${artifact_dir}/capabilities.json" || die "capabilities report does not expose mock parity manifest schema"
grep -q '"id": "mock_parity_report"' "${artifact_dir}/report-schema-registry.json" || die "report schema registry does not catalog mock parity reports"
grep -q '"id": "mock_parity_manifest"' "${artifact_dir}/report-schema-registry.json" || die "report schema registry does not catalog mock parity manifests"

cat >"${artifact_dir}/artifact-index.json" <<'JSON'
{
  "schema_version": "codog.smoke_artifacts.v1",
  "kind": "smoke_artifacts",
  "producer": "scripts/smoke.sh",
  "artifacts": [
    {"path": "version.json", "kind": "version", "producer": "codog --version --json"},
    {"path": "mock-parity-report.json", "kind": "mock_parity_report", "schema_version": "codog.mock_parity.v1", "producer": "codog mock-parity --output-format json"},
    {"path": "mock-parity-stdout.json", "kind": "mock_parity_report", "schema_version": "codog.mock_parity.v1", "producer": "codog mock-parity --output-format json"},
    {"path": "mock-parity-manifest.json", "kind": "mock_parity_manifest", "schema_version": "codog.mock_parity_manifest.v1", "producer": "codog mock-parity manifest --output-format json"},
    {"path": "capabilities.json", "kind": "capabilities", "producer": "codog capabilities --output-format json"},
    {"path": "report-schema-registry.json", "kind": "report_schema_registry", "schema_version": "claw.report.v1", "producer": "codog report-schema registry --output-format json"}
  ]
}
JSON
test -s "${artifact_dir}/artifact-index.json" || die "artifact index was not written"
grep -q '"schema_version": "codog.smoke_artifacts.v1"' "${artifact_dir}/artifact-index.json" || die "artifact index schema version is missing"
grep -q '"path": "mock-parity-manifest.json"' "${artifact_dir}/artifact-index.json" || die "artifact index does not list mock parity manifest"

if [ "${keep_artifacts}" = "1" ] || [ "${artifact_dir_is_temp}" != "1" ]; then
  printf '\nArtifacts written to %s\n' "${artifact_dir}"
fi
