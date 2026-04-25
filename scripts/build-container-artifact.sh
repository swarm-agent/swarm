#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"

DEFAULT_BASE_VERSION="ubuntu24.04-tailscale-stable-v1"
DEFAULT_SOURCE_REPOSITORY="https://github.com/swarm-agent/swarm"
IMAGE_CONTRACT="swarm.container.v1"

usage() {
  cat <<'EOF'
Usage:
  ./scripts/build-container-artifact.sh [options]

Build, inspect, and export the ready-made Swarm container image from an existing
release artifact tree. This is the CI/PR container build path: it does not
rebuild binaries as a fallback, it consumes the exact dist outputs produced by
scripts/build-main-dist.sh.

Options:
  --dist-dir <path>          Release artifact root. Default: ./dist
  --output-dir <path>        Container artifact output dir. Default: <dist>/container
  --base-image-name <ref>    Base image tag. Default: localhost/swarm-base:<base-version>
  --base-version <version>   Slow-changing base contract version.
  --image-name <ref>         App image tag. Default: localhost/swarm:<version-or-sha>
  --source-repository <url>  Official source repository URL.
  --source-revision <sha>    Source revision. Default: commit from build-info.txt
  --github-run-url <url>     GitHub Actions run/provenance URL.
  --runtime <docker|podman>  Container runtime used for build/inspect/save/push. Default: docker
  --push                     Push base and app images after inspection.
  -h, --help                 Show this help text
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

sanitize_tag_part() {
  printf '%s' "${1:-}" | tr -c 'A-Za-z0-9_.-' '-'
}

package_url_for_ref() {
  local ref="${1:-}"
  ref="${ref%%@*}"
  ref="${ref%%:*}"
  case "${ref}" in
    ghcr.io/*/*)
      local rest owner image
      rest="${ref#ghcr.io/}"
      owner="${rest%%/*}"
      image="${rest#*/}"
      if [[ -n "${owner}" && -n "${image}" && "${owner}" != "${image}" ]]; then
        printf 'https://github.com/orgs/%s/packages/container/package/%s\n' "${owner}" "${image//\//%2F}"
      fi
      ;;
  esac
}

image_repo_digests() {
  local image="${1:?image is required}"
  "${RUNTIME}" image inspect "${image}" --format '{{range .RepoDigests}}{{println .}}{{end}}' 2>/dev/null || true
}

preferred_digest_ref() {
  local image="${1:?image is required}"
  local repo="${image%%@*}"
  repo="${repo%%:*}"
  image_repo_digests "${image}" | awk -v repo="${repo}" 'index($0, repo "@") == 1 { print; exit } END { }'
}

runtime_image_label() {
  local image="${1:?image is required}"
  local label="${2:?label is required}"
  "${RUNTIME}" image inspect "${image}" --format "{{ index .Config.Labels \"${label}\" }}" 2>/dev/null || true
}

DIST_DIR="${ROOT_DIR}/dist"
OUTPUT_DIR=""
BASE_VERSION="${DEFAULT_BASE_VERSION}"
BASE_IMAGE_NAME=""
IMAGE_NAME=""
SOURCE_REPOSITORY="${DEFAULT_SOURCE_REPOSITORY}"
SOURCE_REVISION=""
GITHUB_RUN_URL=""
RUNTIME="docker"
PUSH_IMAGES=0

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
    --base-image-name)
      BASE_IMAGE_NAME="${2:-}"
      shift 2
      ;;
    --base-version)
      BASE_VERSION="${2:-}"
      shift 2
      ;;
    --image-name)
      IMAGE_NAME="${2:-}"
      shift 2
      ;;
    --source-repository)
      SOURCE_REPOSITORY="${2:-}"
      shift 2
      ;;
    --source-revision)
      SOURCE_REVISION="${2:-}"
      shift 2
      ;;
    --github-run-url)
      GITHUB_RUN_URL="${2:-}"
      shift 2
      ;;
    --runtime)
      RUNTIME="${2:-}"
      shift 2
      ;;
    --push)
      PUSH_IMAGES=1
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
require_cmd date

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
  "${ROOT_DIR}/deploy/container-mvp/Containerfile.base" \
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
if [[ -z "${SOURCE_REVISION}" ]]; then
  SOURCE_REVISION="${commit}"
fi
short_commit="${commit:0:12}"
safe_version="$(sanitize_tag_part "${version}")"
safe_base_version="$(sanitize_tag_part "${BASE_VERSION}")"
if [[ -z "${BASE_IMAGE_NAME}" ]]; then
  BASE_IMAGE_NAME="localhost/swarm-base:${safe_base_version}"
fi
if [[ -z "${IMAGE_NAME}" ]]; then
  IMAGE_NAME="localhost/swarm:${safe_version}-${short_commit}"
fi

SOURCE_REPOSITORY="${SOURCE_REPOSITORY%/}"
source_commit_url=""
if [[ -n "${SOURCE_REPOSITORY}" && -n "${SOURCE_REVISION}" ]]; then
  source_commit_url="${SOURCE_REPOSITORY}/commit/${SOURCE_REVISION}"
fi
base_package_url="$(package_url_for_ref "${BASE_IMAGE_NAME}")"
image_package_url="$(package_url_for_ref "${IMAGE_NAME}")"
created="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

mkdir -p "${OUTPUT_DIR}" "${ROOT_DIR}/.bin/main" "${ROOT_DIR}/web/dist"

install -m 0755 "${SWARMD_DIST_DIR}/swarmd" "${ROOT_DIR}/.bin/main/swarmd"
install -m 0755 "${SWARMD_DIST_DIR}/swarmctl" "${ROOT_DIR}/.bin/main/swarmctl"
install -m 0755 "${SWARMD_DIST_DIR}/libfff_c.so" "${ROOT_DIR}/swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so"
rm -rf "${ROOT_DIR}/web/dist"
mkdir -p "${ROOT_DIR}/web/dist"
cp -R "${WEB_DIST_DIR}/." "${ROOT_DIR}/web/dist/"

archive_path="${OUTPUT_DIR}/swarm-container-image.tar"
metadata_path="${OUTPUT_DIR}/container-image-info.txt"
rm -f "${archive_path}" "${metadata_path}"

printf '[container-artifact] building base %s\n' "${BASE_IMAGE_NAME}"
(
  cd "${ROOT_DIR}"
  "${RUNTIME}" build \
    --label "org.opencontainers.image.title=Swarm container base" \
    --label "org.opencontainers.image.description=Reusable Swarm base image with OS tools and Tailscale runtime" \
    --label "org.opencontainers.image.source=${SOURCE_REPOSITORY}" \
    --label "org.opencontainers.image.revision=${SOURCE_REVISION}" \
    --label "org.opencontainers.image.version=${BASE_VERSION}" \
    --label "org.opencontainers.image.created=${created}" \
    --label "swarmagent.image.contract=${IMAGE_CONTRACT}" \
    --label "swarmagent.image.role=base" \
    --label "swarmagent.base.version=${BASE_VERSION}" \
    -f deploy/container-mvp/Containerfile.base \
    -t "${BASE_IMAGE_NAME}" \
    .
)

base_image_id="$(${RUNTIME} image inspect "${BASE_IMAGE_NAME}" --format '{{.Id}}')"
base_image_size="$(${RUNTIME} image inspect "${BASE_IMAGE_NAME}" --format '{{.Size}}')"
base_digest_ref=""
if [[ "${PUSH_IMAGES}" == "1" ]]; then
  printf '[container-artifact] pushing base %s\n' "${BASE_IMAGE_NAME}"
  "${RUNTIME}" push "${BASE_IMAGE_NAME}"
  base_digest_ref="$(preferred_digest_ref "${BASE_IMAGE_NAME}")"
fi

printf '[container-artifact] building app %s from base %s\n' "${IMAGE_NAME}" "${BASE_IMAGE_NAME}"
(
  cd "${ROOT_DIR}"
  "${RUNTIME}" build \
    --build-arg "SWARM_BASE_IMAGE=${BASE_IMAGE_NAME}" \
    --label "org.opencontainers.image.title=Swarm runtime" \
    --label "org.opencontainers.image.description=Versioned Swarm runtime layer with Swarm binaries and desktop assets" \
    --label "org.opencontainers.image.source=${SOURCE_REPOSITORY}" \
    --label "org.opencontainers.image.revision=${SOURCE_REVISION}" \
    --label "org.opencontainers.image.version=${version}" \
    --label "org.opencontainers.image.created=${created}" \
    --label "swarmagent.image.contract=${IMAGE_CONTRACT}" \
    --label "swarmagent.image.role=app" \
    --label "swarmagent.version=${version}" \
    --label "swarmagent.commit=${commit}" \
    --label "swarmagent.image.base.ref=${BASE_IMAGE_NAME}" \
    --label "swarmagent.image.base.version=${BASE_VERSION}" \
    --label "swarmagent.image.base.id=${base_image_id}" \
    --label "swarmagent.image.base.digest_ref=${base_digest_ref}" \
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

grep -F "ts_tun_mode=\"\${TS_TUN_MODE:-userspace-networking}\"" /usr/local/bin/swarm-container-entrypoint >/dev/null || {
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

label_contract="$(runtime_image_label "${IMAGE_NAME}" "swarmagent.image.contract")"
label_role="$(runtime_image_label "${IMAGE_NAME}" "swarmagent.image.role")"
label_source="$(runtime_image_label "${IMAGE_NAME}" "org.opencontainers.image.source")"
label_revision="$(runtime_image_label "${IMAGE_NAME}" "org.opencontainers.image.revision")"
if [[ "${label_contract}" != "${IMAGE_CONTRACT}" ]]; then
  echo "image is missing expected contract label: ${IMAGE_CONTRACT}" >&2
  exit 1
fi
if [[ "${label_role}" != "app" ]]; then
  echo "image is missing expected role label: app" >&2
  exit 1
fi
if [[ "${label_source}" != "${SOURCE_REPOSITORY}" ]]; then
  echo "image source label mismatch: ${label_source}" >&2
  exit 1
fi
if [[ "${label_revision}" != "${SOURCE_REVISION}" ]]; then
  echo "image revision label mismatch: ${label_revision}" >&2
  exit 1
fi

image_digest_ref=""
if [[ "${PUSH_IMAGES}" == "1" ]]; then
  printf '[container-artifact] pushing app %s\n' "${IMAGE_NAME}"
  "${RUNTIME}" push "${IMAGE_NAME}"
  image_digest_ref="$(preferred_digest_ref "${IMAGE_NAME}")"
fi

printf '[container-artifact] saving %s to %s\n' "${IMAGE_NAME}" "${archive_path}"
"${RUNTIME}" save -o "${archive_path}" "${IMAGE_NAME}"
tar -tf "${archive_path}" >/dev/null

image_id="$(${RUNTIME} image inspect "${IMAGE_NAME}" --format '{{.Id}}')"
image_size="$(${RUNTIME} image inspect "${IMAGE_NAME}" --format '{{.Size}}')"
archive_size="$(stat -c '%s' "${archive_path}")"

{
  printf 'image_contract=%s\n' "${IMAGE_CONTRACT}"
  printf 'image_ref=%s\n' "${IMAGE_NAME}"
  printf 'image_digest_ref=%s\n' "${image_digest_ref}"
  printf 'image_id=%s\n' "${image_id}"
  printf 'image_size_bytes=%s\n' "${image_size}"
  printf 'image_package_url=%s\n' "${image_package_url}"
  printf 'base_image_ref=%s\n' "${BASE_IMAGE_NAME}"
  printf 'base_image_digest_ref=%s\n' "${base_digest_ref}"
  printf 'base_image_id=%s\n' "${base_image_id}"
  printf 'base_image_size_bytes=%s\n' "${base_image_size}"
  printf 'base_image_package_url=%s\n' "${base_package_url}"
  printf 'base_version=%s\n' "${BASE_VERSION}"
  printf 'archive_path=%s\n' "${archive_path}"
  printf 'archive_size_bytes=%s\n' "${archive_size}"
  printf 'version=%s\n' "${version}"
  printf 'commit=%s\n' "${commit}"
  printf 'source_repository=%s\n' "${SOURCE_REPOSITORY}"
  printf 'source_revision=%s\n' "${SOURCE_REVISION}"
  printf 'source_commit_url=%s\n' "${source_commit_url}"
  printf 'github_run_url=%s\n' "${GITHUB_RUN_URL}"
  printf 'runtime=%s\n' "${RUNTIME}"
  printf 'pushed=%s\n' "${PUSH_IMAGES}"
  printf 'trust_policy=%s\n' "official-source-labels+digest-after-push"
} > "${metadata_path}"

printf '[container-artifact] built image artifact at %s\n' "${archive_path}"
printf '[container-artifact] wrote metadata at %s\n' "${metadata_path}"
