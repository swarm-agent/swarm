#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
GO_LIB="${SCRIPT_DIR}/lib-go.sh"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/build-main-dist.sh [--output-dir <path>] [--skip-web]

Build the same local artifact layout used by .github/workflows/build-main.yml:
  launcher/TUI binaries in <output>/linux-amd64/root
  daemon binaries in <output>/linux-amd64/swarmd
  desktop assets in <output>/web
  metadata in <output>/build-info.txt

Options:
  --output-dir <path>  Artifact root. Default: ./dist
  --skip-web           Skip npm ci, web build, and dist/web staging
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

OUTPUT_DIR="$(cd "$(dirname -- "${OUTPUT_DIR}")" && pwd)/$(basename -- "${OUTPUT_DIR}")"
PLATFORM_DIR="${OUTPUT_DIR}/linux-amd64"
ROOT_ARTIFACT_DIR="${PLATFORM_DIR}/root"
SWARMD_ARTIFACT_DIR="${PLATFORM_DIR}/swarmd"
WEB_ARTIFACT_DIR="${OUTPUT_DIR}/web"

rm -rf "${PLATFORM_DIR}" "${WEB_ARTIFACT_DIR}" "${OUTPUT_DIR}/build-info.txt"
mkdir -p "${ROOT_ARTIFACT_DIR}" "${SWARMD_ARTIFACT_DIR}"

echo "building launcher and TUI binaries into ${ROOT_ARTIFACT_DIR}"
(
  cd "${ROOT_DIR}"
  "${GO_BIN}" build -trimpath -o "${ROOT_ARTIFACT_DIR}/swarmtui" ./cmd/swarmtui
  "${GO_BIN}" build -trimpath -o "${ROOT_ARTIFACT_DIR}/swarm" ./cmd/swarm
  "${GO_BIN}" build -trimpath -ldflags "-X main.defaultInvokedName=swarmdev" -o "${ROOT_ARTIFACT_DIR}/swarmdev" ./cmd/swarm
  "${GO_BIN}" build -trimpath -o "${ROOT_ARTIFACT_DIR}/rebuild" ./cmd/rebuild
  "${GO_BIN}" build -trimpath -o "${ROOT_ARTIFACT_DIR}/swarmsetup" ./cmd/swarmsetup
)

echo "building swarmd binaries into ${SWARMD_ARTIFACT_DIR}"
(
  cd "${ROOT_DIR}/swarmd"
  "${GO_BIN}" build -trimpath -o "${SWARMD_ARTIFACT_DIR}/swarmd" ./cmd/swarmd
  "${GO_BIN}" build -trimpath -o "${SWARMD_ARTIFACT_DIR}/swarmctl" ./cmd/swarmctl
)

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

git_sha="$(git -C "${ROOT_DIR}" rev-parse HEAD 2>/dev/null || printf 'unknown')"
git_ref="$(git -C "${ROOT_DIR}" symbolic-ref -q --short HEAD 2>/dev/null || printf 'detached')"
build_actor="${GITHUB_ACTOR:-local}"
{
  printf 'commit=%s\n' "${git_sha}"
  printf 'actor=%s\n' "${build_actor}"
  printf 'ref=%s\n' "${git_ref}"
  printf 'built_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
} > "${OUTPUT_DIR}/build-info.txt"

echo "built artifact tree at ${OUTPUT_DIR}"
