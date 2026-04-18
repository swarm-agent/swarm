#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
LANE_LIB="${ROOT_DIR}/scripts/lib-lane.sh"
GO_LIB="${ROOT_DIR}/scripts/lib-go.sh"

usage() {
  cat <<'EOF'
Usage:
  ./setup [--with-web] [--start-main]

Options:
  --with-web    install web/ npm dependencies and desktop assets
  --start-main  start the main-lane backend after building
  -h, --help    show this help
EOF
}

require_cmd() {
  local cmd="${1:-}"
  if ! command -v "${cmd}" >/dev/null 2>&1; then
    if [[ "${cmd}" == "rg" ]]; then
      echo "missing required command: rg (ripgrep)" >&2
      echo "Swarm's in-app grep/glob search tools require ripgrep; install it and re-run setup." >&2
    else
      echo "missing required command: ${cmd}" >&2
    fi
    exit 1
  fi
}

require_executable() {
  local path="${1:-}"
  if [[ ! -x "${path}" ]]; then
    echo "expected executable missing after setup: ${path}" >&2
    exit 1
  fi
}

install_desktop_assets() {
  local source_dir="${ROOT_DIR}/web/dist"
  local target_dir
  target_dir="$(swarm_lane_desktop_dist_dir)"

  if [[ ! -f "${source_dir}/index.html" ]]; then
    echo "missing built desktop assets under ${source_dir}" >&2
    echo "run rebuild f after npm install to refresh the installed desktop runtime" >&2
    exit 1
  fi

  rm -rf "${target_dir}"
  mkdir -p "${target_dir}"
  cp -R "${source_dir}/." "${target_dir}/"
  echo "installed desktop assets into ${target_dir}"
}

require_go() {
  local version major minor
  if [[ ! -f "${GO_LIB}" ]]; then
    echo "missing go resolver script at ${GO_LIB}" >&2
    exit 1
  fi
  # shellcheck disable=SC1091
  source "${GO_LIB}"
  swarm_require_go "${ROOT_DIR}"

  version="$("${GO_BIN}" env GOVERSION 2>/dev/null || true)"
  if [[ ! "${version}" =~ ^go([0-9]+)\.([0-9]+) ]]; then
    version="$("${GO_BIN}" version | sed -n 's/^go version go\([0-9][0-9]*\.[0-9][0-9]*\).*/go\1/p' | head -n 1)"
  fi
  if [[ ! "${version}" =~ ^go([0-9]+)\.([0-9]+) ]]; then
    echo "unable to determine Go version from ${GO_BIN}" >&2
    exit 1
  fi

  major="${BASH_REMATCH[1]}"
  minor="${BASH_REMATCH[2]}"
  if (( major < 1 || (major == 1 && minor < 25) )); then
    echo "Go 1.25+ required; found ${version} at ${GO_BIN}" >&2
    exit 1
  fi

  echo "using Go toolchain: ${GO_BIN} (${version})"
  echo "using gofmt: ${GOFMT_BIN}"
}

install_web=0
start_main=0

while [[ "$#" -gt 0 ]]; do
  case "${1}" in
    --with-web)
      install_web=1
      ;;
    --start-main)
      start_main=1
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
  shift
done

require_cmd bash
require_cmd git
require_cmd curl
require_cmd rg
require_go

cd "${ROOT_DIR}"

if [[ ! -f "${LANE_LIB}" ]]; then
  echo "missing lane resolver script at ${LANE_LIB}" >&2
  exit 1
fi
# shellcheck disable=SC1091
source "${LANE_LIB}"

echo "installing swarm/swarmdev/rebuild launchers..."
bash "${ROOT_DIR}/scripts/build-tools.sh"
"$(swarm_lane_tool_bin_dir)/swarmsetup"

echo "building main-lane binaries..."
SWARMD_BUILD_HARD_RESTART=0 SWARM_LANE=main GO_BIN="${GO_BIN}" "${ROOT_DIR}/swarmd/scripts/dev-build.sh"
SWARM_LANE=main GO_BIN="${GO_BIN}" "${ROOT_DIR}/scripts/dev-build.sh"
swarm_lane_export_profile main "${ROOT_DIR}"
require_executable "${SWARM_BIN_DIR}/swarmd"
require_executable "${SWARM_BIN_DIR}/swarmctl"
require_executable "${SWARM_BIN_DIR}/swarmtui"

echo "building dev-lane binaries..."
SWARMD_BUILD_HARD_RESTART=0 SWARM_LANE=dev GO_BIN="${GO_BIN}" "${ROOT_DIR}/swarmd/scripts/dev-build.sh"
SWARM_LANE=dev GO_BIN="${GO_BIN}" "${ROOT_DIR}/scripts/dev-build.sh"
swarm_lane_export_profile dev "${ROOT_DIR}"
require_executable "${SWARM_BIN_DIR}/swarmd"
require_executable "${SWARM_BIN_DIR}/swarmctl"
require_executable "${SWARM_BIN_DIR}/swarmtui"

if [[ "${install_web}" == "1" ]]; then
  require_cmd npm
  echo "installing web dependencies..."
  (
    cd "${ROOT_DIR}/web"
    npm install
    npm run build
  )
  install_desktop_assets
elif [[ -f "${ROOT_DIR}/web/dist/index.html" ]]; then
  echo "installing existing desktop assets..."
  install_desktop_assets
fi

if [[ "${start_main}" == "1" ]]; then
  echo "starting main-lane backend..."
  "$(swarm_lane_tool_bin_dir)/swarm" main backend-up
fi

cat <<EOF
setup complete.

next commands:
  swarm
  swarm dev
  swarm info
  swarm dev info
EOF
