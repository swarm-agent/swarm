#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/build-container-artifact.sh [options]

Build, inspect, and export the ready-made Swarm container image from an existing
release artifact tree. This is the CI/PR container build path: it does not
rebuild binaries as a fallback, it consumes the exact dist outputs produced by
scripts/build-main-dist.sh.

Options:
  --dist-dir <path>       Release artifact root. Default: ./dist
  --output-dir <path>     Container artifact output dir. Default: <dist>/container
  --image-name <ref>      Image tag to build. Default: localhost/swarm-child:<version-or-sha>
  --runtime <docker|podman>
                          Container runtime used for build/inspect/save. Default: docker
  -h, --help              Show this help text
EOF
}

require_cmd() {
  local name="${1:-}"
  command -v "${name}" >/dev/null 2>&1 || {
    echo "required command not found: ${name}" >&2
    exit 1
  }
}

abs_path() {
  local path="${1:?path is required}"
  mkdir -p "$(dirname -- "${path}")"
  (cd "$(dirname -- "${path}")" && printf '%s/%s\n' "$(pwd)" "$(basename -- "${path}")")
}

DIST_DIR="${ROOT_DIR}/dist"
OUTPUT_DIR=""
IMAGE_NAME=""
RUNTIME="docker"

while [[ $# -gt 0 ]]; do
  case "${1}" in
    --dist-dir)
      DIST_DIR="${2:-}"
      shift 2
      ;;
    --output-dir)
      OUTPUT_DIR="${2:-}"
      shift 2
      ;;
    --image-name)
      IMAGE_NAME="${2:-}"
      shift 2
      ;;
    --runtime)
      RUNTIME="${2:-}"
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

case "${RUNTIME}" in
  docker|podman)
    ;;
  *)
    echo "unsupported runtime: ${RUNTIME} (expected docker or podman)" >&2
    exit 2
    ;;
esac

require_cmd "${RUNTIME}"
require_cmd awk
require_cmd find
require_cmd stat
require_cmd tar

DIST_DIR="$(abs_path "${DIST_DIR}")"
if [[ -z "${OUTPUT_DIR}" ]]; then
  OUTPUT_DIR="${DIST_DIR}/container"
fi
OUTPUT_DIR="$(abs_path "${OUTPUT_DIR}")"

BUILD_INFO="${DIST_DIR}/build-info.txt"
SWARMD_DIST_DIR="${DIST_DIR}/linux-amd64/swarmd"
WEB_DIST_DIR="${DIST_DIR}/web"

for required_path in \
  "${BUILD_INFO}" \
  "${SWARMD_DIST_DIR}/swarmd" \
  "${SWARMD_DIST_DIR}/swarmctl" \
  "${SWARMD_DIST_DIR}/libfff_c.so" \
  "${WEB_DIST_DIR}" \
  "${ROOT_DIR}/deploy/container-mvp/Containerfile" \
  "${ROOT_DIR}/deploy/container-mvp/entrypoint.sh"
do
  if [[ ! -e "${required_path}" ]]; then
    echo "missing required container build input: ${required_path}" >&2
    exit 1
  fi
done

version="$(awk -F= '$1 == "version" { print $2; exit }' "${BUILD_INFO}")"
commit="$(awk -F= '$1 == "commit" { print $2; exit }' "${BUILD_INFO}")"
if [[ -z "${version}" ]]; then
  echo "build-info.txt is missing version" >&2
  exit 1
fi
if [[ -z "${commit}" ]]; then
  echo "build-info.txt is missing commit" >&2
  exit 1
fi
short_commit="${commit:0:12}"
if [[ -z "${IMAGE_NAME}" ]]; then
  safe_version="$(printf '%s' "${version}" | tr -c 'A-Za-z0-9_.-' '-')"
  IMAGE_NAME="localhost/swarm-child:${safe_version}-${short_commit}"
fi

mkdir -p "${OUTPUT_DIR}" "${ROOT_DIR}/.bin/main" "${ROOT_DIR}/web/dist"

install -m 0755 "${SWARMD_DIST_DIR}/swarmd" "${ROOT_DIR}/.bin/main/swarmd"
install -m 0755 "${SWARMD_DIST_DIR}/swarmctl" "${ROOT_DIR}/.bin/main/swarmctl"
rm -rf "${ROOT_DIR}/web/dist"
mkdir -p "${ROOT_DIR}/web/dist"
cp -R "${WEB_DIST_DIR}/." "${ROOT_DIR}/web/dist/"

archive_path="${OUTPUT_DIR}/swarm-container-image.tar"
metadata_path="${OUTPUT_DIR}/container-image-info.txt"
rm -f "${archive_path}" "${metadata_path}"

printf '[container-artifact] building %s from dist %s\n' "${IMAGE_NAME}" "${DIST_DIR}"
(
  cd "${ROOT_DIR}"
  "${RUNTIME}" build \
    --label "swarmagent.version=${version}" \
    --label "swarmagent.commit=${commit}" \
    -f deploy/container-mvp/Containerfile \
    -t "${IMAGE_NAME}" \
    .
)

printf '[container-artifact] inspecting %s\n' "${IMAGE_NAME}"
"${RUNTIME}" run --rm --entrypoint bash "${IMAGE_NAME}" -lc '
set -euo pipefail

required_execs=(
  /usr/local/bin/swarmd
  /usr/local/bin/swarmctl
  /usr/local/bin/tailscale
  /usr/local/bin/tailscaled
  /usr/local/bin/swarm-container-entrypoint
)
required_files=(
  /usr/local/lib/libfff_c.so
)
required_dirs=(
  /opt/swarm/web/dist
  /var/lib/swarmd/home
)

for path in "${required_execs[@]}"; do
  [[ -x "${path}" ]] || { echo "missing required executable: ${path}" >&2; exit 1; }
done
for path in "${required_files[@]}"; do
  [[ -f "${path}" ]] || { echo "missing required file: ${path}" >&2; exit 1; }
done
for path in "${required_dirs[@]}"; do
  [[ -d "${path}" ]] || { echo "missing required directory: ${path}" >&2; exit 1; }
done

owner_check="$(stat -c "%U:%G" /var/lib/swarmd /var/run/swarmd /var/lib/swarmd/home | sort -u)"
if [[ "${owner_check}" != "nobody:nogroup" ]]; then
  echo "unexpected internal runtime directory ownership: ${owner_check}" >&2
  exit 1
fi

grep -F 'ts_tun_mode="${TS_TUN_MODE:-userspace-networking}"' /usr/local/bin/swarm-container-entrypoint >/dev/null || {
  echo "entrypoint no longer defaults TS_TUN_MODE to userspace-networking" >&2
  exit 1
}

forbidden_hits="$(
  find /usr/local /opt/swarm /root /workspaces \
    \( \
      -name .git -o \
      -name .env -o \
      -name .swarmenv -o \
      -name .cache -o \
      -name .docker -o \
      -name .npmrc -o \
      -name .ssh \
    \) \
    -print | sort
)"
if [[ -n "${forbidden_hits}" ]]; then
  echo "forbidden local-only paths found in image:" >&2
  printf "%s\n" "${forbidden_hits}" >&2
  exit 1
fi

# Smoke the dynamic linker without starting the daemon.
ldd /usr/local/bin/swarmd >/dev/null
ldd /usr/local/bin/swarmctl >/dev/null
'

printf '[container-artifact] saving %s to %s\n' "${IMAGE_NAME}" "${archive_path}"
"${RUNTIME}" save -o "${archive_path}" "${IMAGE_NAME}"
tar -tf "${archive_path}" >/dev/null

image_id="$("${RUNTIME}" image inspect "${IMAGE_NAME}" --format '{{.Id}}')"
image_size="$("${RUNTIME}" image inspect "${IMAGE_NAME}" --format '{{.Size}}')"
archive_size="$(stat -c '%s' "${archive_path}")"

{
  printf 'image_ref=%s\n' "${IMAGE_NAME}"
  printf 'image_id=%s\n' "${image_id}"
  printf 'image_size_bytes=%s\n' "${image_size}"
  printf 'archive_path=%s\n' "${archive_path}"
  printf 'archive_size_bytes=%s\n' "${archive_size}"
  printf 'version=%s\n' "${version}"
  printf 'commit=%s\n' "${commit}"
  printf 'runtime=%s\n' "${RUNTIME}"
} > "${metadata_path}"

printf '[container-artifact] built image artifact at %s\n' "${archive_path}"
printf '[container-artifact] wrote metadata at %s\n' "${metadata_path}"
