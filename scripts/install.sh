#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage: scripts/install.sh [options]

Builds Codog from this checkout and installs the single codog binary.

Options:
  --prefix DIR       Install under DIR/bin. Defaults to ~/.local.
  --bin-dir DIR      Install directly into DIR. Overrides --prefix.
  --no-verify        Skip the post-install version smoke check.
  -h, --help         Show this help text.

Environment:
  CODOG_INSTALL_PREFIX   Default install prefix when --prefix is not set.
  CODOG_INSTALL_BIN_DIR  Default bin directory when --bin-dir is not set.
  CODOG_INSTALL_VERIFY   Set to 0 to skip the version smoke check.
EOF
}

die() {
  printf 'install: %s\n' "$*" >&2
  exit 2
}

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "${script_dir}/.." && pwd)"
prefix="${CODOG_INSTALL_PREFIX:-${HOME}/.local}"
bin_dir="${CODOG_INSTALL_BIN_DIR:-}"
verify="${CODOG_INSTALL_VERIFY:-1}"

while [ "$#" -gt 0 ]; do
  case "$1" in
    --prefix)
      shift
      [ "$#" -gt 0 ] || die "--prefix requires a directory"
      prefix="$1"
      ;;
    --prefix=*)
      prefix="${1#--prefix=}"
      ;;
    --bin-dir)
      shift
      [ "$#" -gt 0 ] || die "--bin-dir requires a directory"
      bin_dir="$1"
      ;;
    --bin-dir=*)
      bin_dir="${1#--bin-dir=}"
      ;;
    --no-verify)
      verify="0"
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

[ -n "${prefix}" ] || die "install prefix cannot be empty"
if [ -z "${bin_dir}" ]; then
  bin_dir="${prefix}/bin"
fi
[ -n "${bin_dir}" ] || die "install bin directory cannot be empty"

command -v go >/dev/null 2>&1 || die "go is required"

git_sha="unknown"
git_branch="unknown"
git_dirty="unknown"
if command -v git >/dev/null 2>&1 && git -C "${repo_root}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  git_sha="$(git -C "${repo_root}" rev-parse HEAD 2>/dev/null || printf 'unknown')"
  git_branch="$(git -C "${repo_root}" rev-parse --abbrev-ref HEAD 2>/dev/null || printf 'unknown')"
  if git -C "${repo_root}" diff --quiet --ignore-submodules -- 2>/dev/null &&
     git -C "${repo_root}" diff --cached --quiet --ignore-submodules -- 2>/dev/null; then
    git_dirty="false"
  else
    git_dirty="true"
  fi
fi

build_date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags=(
  "-X" "github.com/Rememorio/codog/internal/versioninfo.GitSHA=${git_sha}"
  "-X" "github.com/Rememorio/codog/internal/versioninfo.GitBranch=${git_branch}"
  "-X" "github.com/Rememorio/codog/internal/versioninfo.GitDirty=${git_dirty}"
  "-X" "github.com/Rememorio/codog/internal/versioninfo.BuildDate=${build_date}"
)

tmp_dir="$(mktemp -d)"
cleanup() {
  rm -rf "${tmp_dir}"
}
trap cleanup EXIT

mkdir -p "${bin_dir}"
tmp_binary="${tmp_dir}/codog"
target="${bin_dir}/codog"

printf 'Building codog from %s\n' "${repo_root}"
(
  cd "${repo_root}"
  go build -trimpath -ldflags "${ldflags[*]}" -o "${tmp_binary}" .
)

install -m 0755 "${tmp_binary}" "${target}"

if [ "${verify}" != "0" ]; then
  "${target}" --version >/dev/null
fi

printf 'Installed codog to %s\n' "${target}"
