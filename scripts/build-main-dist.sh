#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
GO_LIB="${SCRIPT_DIR}/lib-go.sh"
MODULE_PATH="$(awk '/^module / { print $2; exit }' "${ROOT_DIR}/go.mod")"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/build-main-dist.sh [--output-dir <path>] [--skip-web] [--version <value>]

Build the same local artifact layout used by .github/workflows/build-main.yml:
  launcher/TUI binaries in <output>/linux-amd64/root
  daemon binaries in <output>/linux-amd64/swarmd
  desktop assets in <output>/web
  metadata in <output>/build-info.txt
  installer script at <output>/release-stage/swarm-<version>-linux-amd64/install.sh
  release archive at <output>/swarm-<version>-linux-amd64.tar.gz

Options:
  --output-dir <path>  Artifact root. Default: ./dist
  --skip-web           Skip npm ci, web build, and dist/web staging
  --version <value>    Release version to embed/package. Default: derived from git
  -h, --help           Show this help text
EOF
}

require_cmd() {
  local name="${1:-}"
  command -v "${name}" >/dev/null 2>&1 || {
    echo "required command not found: ${name}" >&2
    exit 1
  }
}

OUTPUT_DIR="${ROOT_DIR}/dist"
BUILD_WEB="true"
REQUESTED_VERSION=""

while [[ $# -gt 0 ]]; do
  case "${1}" in
    --output-dir)
      OUTPUT_DIR="${2:-}"
      shift 2
      ;;
    --skip-web)
      BUILD_WEB="false"
      shift
      ;;
    --version)
      REQUESTED_VERSION="${2:-}"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unsupported argument: ${1}" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ ! -f "${GO_LIB}" ]]; then
  echo "missing go resolver script at ${GO_LIB}" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${GO_LIB}"

swarm_require_go "${ROOT_DIR}"
require_cmd git
require_cmd tar
require_cmd awk

OUTPUT_DIR="$(cd "$(dirname -- "${OUTPUT_DIR}")" && pwd)/$(basename -- "${OUTPUT_DIR}")"
PLATFORM_DIR="${OUTPUT_DIR}/linux-amd64"
ROOT_ARTIFACT_DIR="${PLATFORM_DIR}/root"
SWARMD_ARTIFACT_DIR="${PLATFORM_DIR}/swarmd"
WEB_ARTIFACT_DIR="${OUTPUT_DIR}/web"
RELEASE_STAGE_DIR="${OUTPUT_DIR}/release-stage"

rm -rf "${PLATFORM_DIR}" "${WEB_ARTIFACT_DIR}" "${RELEASE_STAGE_DIR}" "${OUTPUT_DIR}/build-info.txt" "${OUTPUT_DIR}"/swarm-*.tar.gz
mkdir -p "${ROOT_ARTIFACT_DIR}" "${SWARMD_ARTIFACT_DIR}"

git_sha="$(git -C "${ROOT_DIR}" rev-parse HEAD 2>/dev/null || printf 'unknown')"
git_ref="$(git -C "${ROOT_DIR}" symbolic-ref -q --short HEAD 2>/dev/null || printf 'detached')"
git_tag="$(git -C "${ROOT_DIR}" describe --tags --exact-match 2>/dev/null || true)"
if [[ -n "${REQUESTED_VERSION}" ]]; then
  release_version="${REQUESTED_VERSION}"
elif [[ -n "${git_tag}" ]]; then
  if [[ "${git_tag}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    release_version="${git_tag}"
  else
    echo "release build requires a stable semver tag vX.Y.Z; found exact tag ${git_tag}" >&2
    exit 1
  fi
else
  release_version="$("${SCRIPT_DIR}/resolve-release-version.sh")"
fi
build_actor="${GITHUB_ACTOR:-local}"
built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
ldflags=(
  -X "${MODULE_PATH}/internal/buildinfo.Version=${release_version}"
  -X "${MODULE_PATH}/internal/buildinfo.Commit=${git_sha}"
  -X "${MODULE_PATH}/internal/buildinfo.BuiltAt=${built_at}"
  -X "${MODULE_PATH}/pkg/buildinfo.Version=${release_version}"
  -X "${MODULE_PATH}/pkg/buildinfo.Commit=${git_sha}"
  -X "${MODULE_PATH}/pkg/buildinfo.BuiltAt=${built_at}"
)

echo "building launcher and TUI binaries into ${ROOT_ARTIFACT_DIR}"
(
  cd "${ROOT_DIR}"
  "${GO_BIN}" build -trimpath -ldflags "${ldflags[*]}" -o "${ROOT_ARTIFACT_DIR}/swarmtui" ./cmd/swarmtui
  "${GO_BIN}" build -trimpath -ldflags "${ldflags[*]}" -o "${ROOT_ARTIFACT_DIR}/swarm" ./cmd/swarm
  "${GO_BIN}" build -trimpath -ldflags "${ldflags[*]} -X main.defaultInvokedName=swarmdev" -o "${ROOT_ARTIFACT_DIR}/swarmdev" ./cmd/swarm
  "${GO_BIN}" build -trimpath -ldflags "${ldflags[*]}" -o "${ROOT_ARTIFACT_DIR}/rebuild" ./cmd/rebuild
  "${GO_BIN}" build -trimpath -ldflags "${ldflags[*]}" -o "${ROOT_ARTIFACT_DIR}/swarmsetup" ./cmd/swarmsetup
)

echo "building swarmd binaries into ${SWARMD_ARTIFACT_DIR}"
(
  cd "${ROOT_DIR}/swarmd"
  "${GO_BIN}" build -trimpath -ldflags "${ldflags[*]}" -o "${SWARMD_ARTIFACT_DIR}/swarmd" ./cmd/swarmd
  "${GO_BIN}" build -trimpath -ldflags "${ldflags[*]}" -o "${SWARMD_ARTIFACT_DIR}/swarmctl" ./cmd/swarmctl
)
cp "${ROOT_DIR}/swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so" "${SWARMD_ARTIFACT_DIR}/libfff_c.so"

if [[ "${BUILD_WEB}" == "true" ]]; then
  require_cmd node
  require_cmd npm
  echo "building desktop assets into ${WEB_ARTIFACT_DIR}"
  (
    cd "${ROOT_DIR}/web"
    npm ci
    npm run build
  )
  mkdir -p "${WEB_ARTIFACT_DIR}"
  cp -R "${ROOT_DIR}/web/dist/." "${WEB_ARTIFACT_DIR}/"
fi

{
  printf 'version=%s\n' "${release_version}"
  printf 'commit=%s\n' "${git_sha}"
  printf 'actor=%s\n' "${build_actor}"
  printf 'ref=%s\n' "${git_ref}"
  printf 'built_at=%s\n' "${built_at}"
} > "${OUTPUT_DIR}/build-info.txt"

archive_basename="swarm-${release_version}-linux-amd64"
archive_path="${OUTPUT_DIR}/${archive_basename}.tar.gz"
mkdir -p "${RELEASE_STAGE_DIR}/${archive_basename}"
cp -R "${PLATFORM_DIR}" "${RELEASE_STAGE_DIR}/${archive_basename}/linux-amd64"
if [[ -d "${WEB_ARTIFACT_DIR}" ]]; then
  cp -R "${WEB_ARTIFACT_DIR}" "${RELEASE_STAGE_DIR}/${archive_basename}/web"
fi
cp "${OUTPUT_DIR}/build-info.txt" "${RELEASE_STAGE_DIR}/${archive_basename}/build-info.txt"
cp "${ROOT_DIR}/install.sh" "${RELEASE_STAGE_DIR}/${archive_basename}/install.sh"
chmod 755 "${RELEASE_STAGE_DIR}/${archive_basename}/install.sh"
(
  cd "${RELEASE_STAGE_DIR}"
  tar -czf "${archive_path}" "${archive_basename}"
)

echo "built artifact tree at ${OUTPUT_DIR}"
echo "built release archive at ${archive_path}"
