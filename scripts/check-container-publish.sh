#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${ROOT_DIR}"

RUNTIME="docker"
IMAGE_NAME=""
SKIP_IMAGE_CLEANUP=0
FORBIDDEN_TOKENS=()
REMOTE_ARGS=()

usage() {
  cat <<'EOF'
Usage: ./scripts/check-container-publish.sh [options] -- [remote deploy harness args...]

Mandatory pre-GitHub gate for remote deploy container changes:
  1. run launch-readiness + precommit + CVE scans
  2. verify .dockerignore excludes local-only build context paths
  3. build the base + app container images through scripts/rebuild-container.sh --image-only
  4. inspect the built image for required runtime files, trust labels, and forbidden local-only paths
  5. run the checked-in remote deploy E2E harness with routed proof and teardown

Examples:
  ./scripts/check-container-publish.sh --runtime docker -- \
    --ssh-target my-lan-host \
    --transport-mode lan \
    --remote-advertise-host 10.0.0.44

  ./scripts/check-container-publish.sh --runtime docker -- \
    --ssh-target my-tailnet-host \
    --transport-mode tailscale \
    --tailscale-auth-mode key \
    --tailscale-auth-key-env SWARM_TS_AUTHKEY

Options:
  --runtime <docker|podman>   Container runtime used for image build/inspection. Default: docker
  --image-name <tag>          Temporary image tag. Default: localhost/swarm-container-publish-check:<timestamp>
  --forbid-token <value>      Exact token that must not appear in tracked files. Repeatable.
  --skip-image-cleanup        Leave the temporary inspection image behind after the script exits
  -h, --help                  Show this help text

Notes:
  - Pass the checked-in remote deploy harness arguments after `--`.
  - Raw secrets must come from env-name flags consumed by the harness. Do not place real secrets on the command line.
  - This gate rejects `--no-wait` and `--teardown-only` because they do not prove the full end-to-end path.
EOF
}

need_cmd() {
  local name="${1:-}"
  command -v "${name}" >/dev/null 2>&1 || {
    echo "[container-publish] missing required command: ${name}" >&2
    exit 1
  }
}

log_step() {
  printf '[container-publish] %s\n' "$*"
}

die() {
  printf '[container-publish] FAIL: %s\n' "$*" >&2
  exit 1
}

has_arg() {
  local needle="${1:-}"
  shift || true
  local value
  for value in "$@"; do
    if [[ "${value}" == "${needle}" ]]; then
      return 0
    fi
  done
  return 1
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --runtime)
      [[ $# -ge 2 ]] || die "missing value for --runtime"
      RUNTIME="$2"
      shift
      ;;
    --image-name)
      [[ $# -ge 2 ]] || die "missing value for --image-name"
      IMAGE_NAME="$2"
      shift
      ;;
    --forbid-token)
      [[ $# -ge 2 ]] || die "missing value for --forbid-token"
      FORBIDDEN_TOKENS+=("$2")
      shift
      ;;
    --skip-image-cleanup)
      SKIP_IMAGE_CLEANUP=1
      ;;
    --)
      shift
      REMOTE_ARGS=("$@")
      break
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

case "${RUNTIME}" in
  docker|podman)
    ;;
  *)
    die "unsupported runtime: ${RUNTIME} (expected docker or podman)"
    ;;
esac

if [[ -z "${IMAGE_NAME}" ]]; then
  IMAGE_NAME="localhost/swarm-container-publish-check:$(date -u +%Y%m%d%H%M%S)"
fi

need_cmd bash
need_cmd git
need_cmd rg
need_cmd find
need_cmd mktemp
need_cmd "${RUNTIME}"

cleanup() {
  if [[ "${SKIP_IMAGE_CLEANUP}" == "1" ]]; then
    return
  fi
  "${RUNTIME}" image rm -f "${IMAGE_NAME}" >/dev/null 2>&1 || true
}
trap cleanup EXIT

require_dockerignore_line() {
  local expected="${1:-}"
  if ! rg -qxF -- "${expected}" "${ROOT_DIR}/.dockerignore"; then
    die ".dockerignore is missing required entry: ${expected}"
  fi
}

prepare_remote_args() {
  if (( ${#REMOTE_ARGS[@]} == 0 )); then
    die "missing remote deploy harness arguments after --"
  fi
  has_arg "--ssh-target" "${REMOTE_ARGS[@]}" || die "remote deploy harness args must include --ssh-target"
  if has_arg "--no-wait" "${REMOTE_ARGS[@]}"; then
    die "--no-wait is not allowed in the publish gate"
  fi
  if has_arg "--teardown-only" "${REMOTE_ARGS[@]}"; then
    die "--teardown-only is not allowed in the publish gate"
  fi
  if ! has_arg "--prove-routed-ai" "${REMOTE_ARGS[@]}"; then
    REMOTE_ARGS+=(--prove-routed-ai)
  fi
  if ! has_arg "--teardown" "${REMOTE_ARGS[@]}"; then
    REMOTE_ARGS+=(--teardown)
  fi
  if ! has_arg "--remote-runtime" "${REMOTE_ARGS[@]}"; then
    REMOTE_ARGS+=(--remote-runtime "${RUNTIME}")
  fi
}

run_launch_readiness() {
  local cmd=(bash "${ROOT_DIR}/scripts/check-launch-readiness.sh" --require-clean)
  local token
  for token in "${FORBIDDEN_TOKENS[@]}"; do
    cmd+=(--forbid-token "${token}")
  done
  log_step "running launch-readiness, secret scans, and CVE checks"
  "${cmd[@]}"
}

check_build_context() {
  [[ -f "${ROOT_DIR}/.dockerignore" ]] || die "missing .dockerignore"
  log_step "verifying .dockerignore excludes local-only build context paths"
  require_dockerignore_line ".git"
  require_dockerignore_line ".cache"
  require_dockerignore_line "dist"
  require_dockerignore_line ".runtime"
  require_dockerignore_line ".swarm"
  require_dockerignore_line ".tmp"
  require_dockerignore_line ".tmp-tools"
  require_dockerignore_line "tmp"
  require_dockerignore_line ".tools/go"
  require_dockerignore_line ".tools/bin"
  require_dockerignore_line "web/node_modules"
  require_dockerignore_line "web/src"
  require_dockerignore_line "web/scripts"
  require_dockerignore_line "web/public"
  require_dockerignore_line "swarmd/.cache"
  require_dockerignore_line "swarmd/.data"
  require_dockerignore_line "swarmd/tmp"
}

build_image() {
  log_step "building inspection image ${IMAGE_NAME}"
  env BUILD_RUNTIME="${RUNTIME}" IMAGE_NAME="${IMAGE_NAME}" \
    bash "${ROOT_DIR}/scripts/rebuild-container.sh" --image-only
}

inspect_image() {
  log_step "inspecting built image for required runtime files, trust labels, and forbidden local-only paths"
  local contract role
  contract="$(${RUNTIME} image inspect "${IMAGE_NAME}" --format '{{ index .Config.Labels "swarmagent.image.contract" }}' 2>/dev/null || true)"
  role="$(${RUNTIME} image inspect "${IMAGE_NAME}" --format '{{ index .Config.Labels "swarmagent.image.role" }}' 2>/dev/null || true)"
  if [[ "${contract}" != "swarm.container.v1" ]]; then
    die "image ${IMAGE_NAME} is missing swarm.container.v1 contract label"
  fi
  if [[ "${role}" != "app" ]]; then
    die "image ${IMAGE_NAME} is missing app role label"
  fi
  "${RUNTIME}" run --rm --entrypoint bash "${IMAGE_NAME}" -lc '
set -euo pipefail

join_path() {
  local out="" part
  for part in "$@"; do
    out="${out}/${part}"
  done
  printf "%s\n" "${out:-/}"
}

required_execs=(
  /usr/local/bin/swarmd
  /usr/local/bin/swarmctl
  /usr/local/bin/swarm-fff-search
  /usr/local/bin/tailscale
  /usr/local/bin/tailscaled
  /usr/local/bin/swarm-container-entrypoint
)

required_files=(
  /usr/local/lib/libfff_c.so
)

required_dirs=(
  "$(join_path opt swarm web dist)"
  /var/lib/swarmd/home
)

for path in "${required_execs[@]}"; do
  [[ -x "${path}" ]] || {
    echo "missing required executable: ${path}" >&2
    exit 1
  }
done

for path in "${required_files[@]}"; do
  [[ -f "${path}" ]] || {
    echo "missing required file: ${path}" >&2
    exit 1
  }
done

for path in "${required_dirs[@]}"; do
  [[ -d "${path}" ]] || {
    echo "missing required directory: ${path}" >&2
    exit 1
  }
done

owner_check="$(stat -c '%U:%G' /var/lib/swarmd /var/run/swarmd /var/lib/swarmd/home | sort -u)"
if [[ "${owner_check}" != "nobody:nogroup" ]]; then
  echo "unexpected internal runtime directory ownership: ${owner_check}" >&2
  exit 1
fi

workspace_owner="$(stat -c '%U:%G' /workspaces)"
echo "[container-publish] workspace mount root intentionally left shared-host owned: ${workspace_owner}"

grep -F 'ts_tun_mode="${TS_TUN_MODE:-userspace-networking}"' /usr/local/bin/swarm-container-entrypoint >/dev/null || {
  echo "entrypoint no longer defaults TS_TUN_MODE to userspace-networking" >&2
  exit 1
}

scan_roots=()
opt_swarm_root="$(join_path opt swarm)"
for root in /usr/local "${opt_swarm_root}" /root /workspaces; do
  [[ -e "${root}" ]] && scan_roots+=("${root}")
done

if ((${#scan_roots[@]} == 0)); then
  echo "no scan roots found inside the image" >&2
  exit 1
fi

forbidden_hits="$(
  find "${scan_roots[@]}" \
    \( \
      -name ".git" -o \
      -name ".env" -o \
      -name ".swarmenv" -o \
      -name ".cache" -o \
      -name ".docker" -o \
      -name ".npmrc" -o \
      -name ".ssh" \
    \) \
    -print | sort
)"

if [[ -n "${forbidden_hits}" ]]; then
  echo "forbidden local-only paths found in image:" >&2
  printf "%s\n" "${forbidden_hits}" >&2
  exit 1
fi
'
}

run_remote_e2e() {
  log_step "running checked-in remote deploy E2E harness"
  bash "${ROOT_DIR}/tests/swarmd/remote_deploy_e2e.sh" "${REMOTE_ARGS[@]}"
}

prepare_remote_args
run_launch_readiness
check_build_context
build_image
inspect_image
run_remote_e2e

log_step "PASS"
