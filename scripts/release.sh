#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/release.sh --version VERSION [options]

Builds cross-platform Codog release archives and SHA256SUMS.

Options:
  --version VERSION  Release version without a leading v (required).
  --commit REF       Git commit or tag to build. Defaults to HEAD.
  --output-dir DIR   Artifact directory. Defaults to dist.
  -h, --help         Show this help text.

Environment:
  CODOG_RELEASE_BRANCH  Branch recorded in build metadata. Defaults to main.
  CODOG_BUILD_PACKAGE  Go package to build. Defaults to ./cmd/codog.
EOF
}

die() {
  printf 'release: %s\n' "$*" >&2
  exit 2
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
version=""
commit="HEAD"
output_dir="${repo_root}/dist"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version)
      shift
      [ "$#" -gt 0 ] || die "--version requires a value"
      version="$1"
      ;;
    --version=*)
      version="${1#--version=}"
      ;;
    --commit)
      shift
      [ "$#" -gt 0 ] || die "--commit requires a ref"
      commit="$1"
      ;;
    --commit=*)
      commit="${1#--commit=}"
      ;;
    --output-dir)
      shift
      [ "$#" -gt 0 ] || die "--output-dir requires a directory"
      output_dir="$1"
      ;;
    --output-dir=*)
      output_dir="${1#--output-dir=}"
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

[[ "${version}" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]] ||
  die "--version must be a semantic version without a leading v"

for command in git go tar zip; do
  command -v "${command}" >/dev/null 2>&1 || die "${command} is required"
done

cd "${repo_root}"

if [ -n "$(git status --porcelain --untracked-files=no)" ]; then
  die "tracked files must be clean before building a release"
fi

commit_sha="$(git rev-parse "${commit}^{commit}")" || die "cannot resolve commit ${commit}"
head_sha="$(git rev-parse HEAD)"
[ "${commit_sha}" = "${head_sha}" ] || die "release commit must match the checked-out HEAD"

source_version="$(sed -n 's/^const version = "\([^"]*\)".*/\1/p' internal/agent/agent.go)"
[ -n "${source_version}" ] || die "cannot read the Codog version from internal/agent/agent.go"
[ "${source_version}" = "${version}" ] ||
  die "release version ${version} does not match source version ${source_version}"

mkdir -p "${output_dir}"
output_dir="$(cd -- "${output_dir}" && pwd)"
if find "${output_dir}" -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
  die "output directory must be empty: ${output_dir}"
fi

staging_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${staging_dir}"
}
trap cleanup EXIT

build_date="$(git show -s --format=%cI "${commit_sha}")"
branch="${CODOG_RELEASE_BRANCH:-main}"
build_package="${CODOG_BUILD_PACKAGE:-./cmd/codog}"
host_os="$(go env GOOS)"
host_arch="$(go env GOARCH)"
ldflags="-s -w"
ldflags+=" -X github.com/Rememorio/codog/internal/versioninfo.GitSHA=${commit_sha}"
ldflags+=" -X github.com/Rememorio/codog/internal/versioninfo.GitBranch=${branch}"
ldflags+=" -X github.com/Rememorio/codog/internal/versioninfo.GitDirty=false"
ldflags+=" -X github.com/Rememorio/codog/internal/versioninfo.BuildDate=${build_date}"

targets=(
  darwin/amd64
  darwin/arm64
  linux/amd64
  linux/arm64
  windows/amd64
  windows/arm64
)
artifacts=()

for target in "${targets[@]}"; do
  goos="${target%/*}"
  goarch="${target#*/}"
  package_name="codog_${version}_${goos}_${goarch}"
  package_dir="${staging_dir}/${package_name}"
  binary_name="codog"
  archive_name="${package_name}.tar.gz"
  if [ "${goos}" = "windows" ]; then
    binary_name="codog.exe"
    archive_name="${package_name}.zip"
  fi

  mkdir -p "${package_dir}"
  printf 'Building %s/%s\n' "${goos}" "${goarch}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go build -trimpath -ldflags "${ldflags}" -o "${package_dir}/${binary_name}" "${build_package}"
  cp README.md LICENSE "${package_dir}/"

  if [ "${goos}" = "${host_os}" ] && [ "${goarch}" = "${host_arch}" ]; then
    version_json="$("${package_dir}/${binary_name}" --version --json)"
    printf '%s\n' "${version_json}" | grep -q '"version": "'"${version}"'"' ||
      die "native artifact reports an unexpected version"
    printf '%s\n' "${version_json}" | grep -q '"git_sha": "'"${commit_sha}"'"' ||
      die "native artifact reports an unexpected git SHA"
  fi

  if [ "${goos}" = "windows" ]; then
    (
      cd "${staging_dir}"
      zip -q -r "${output_dir}/${archive_name}" "${package_name}"
    )
  else
    tar -C "${staging_dir}" -czf "${output_dir}/${archive_name}" "${package_name}"
  fi
  artifacts+=("${archive_name}")
done

(
  cd "${output_dir}"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${artifacts[@]}" >SHA256SUMS
  else
    shasum -a 256 "${artifacts[@]}" >SHA256SUMS
  fi
)

printf 'Release artifacts written to %s\n' "${output_dir}"
