#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/test-install-distro.sh --archive <archive.tar.gz> --distro <ubuntu|arch|omarchy>

Run a release archive through a fresh privileged distro environment. The runner
installs curl first, creates a non-root service owner with passwordless sudo,
unsets TMPDIR, runs the archive's public install.sh in systemd service mode,
and verifies the installed CLI and active service.

Environment:
  SWARM_INSTALL_DISTRO_RUNTIME  Container runtime command (default: podman, then docker)
  SWARM_INSTALL_UBUNTU_IMAGE    Ubuntu image (default: ubuntu:24.04)
  SWARM_INSTALL_ARCH_IMAGE      Arch image (default: archlinux:base)

Omarchy publishes a full installation ISO rather than an OCI image; use
scripts/test-install-omarchy-vm.sh against a clean official-ISO VM overlay.
EOF
}

fail() {
  printf 'test-install-distro: %s\n' "$*" >&2
  exit 1
}

ARCHIVE=""
DISTRO=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --archive) [[ $# -ge 2 ]] || fail "--archive requires a path"; ARCHIVE="$2"; shift 2 ;;
    --distro) [[ $# -ge 2 ]] || fail "--distro requires a value"; DISTRO="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unsupported argument: $1" ;;
  esac
done

[[ -n "${ARCHIVE}" ]] || fail "--archive is required"
[[ -f "${ARCHIVE}" ]] || fail "archive does not exist: ${ARCHIVE}"
case "${DISTRO}" in
  ubuntu|arch) ;;
  omarchy) fail "Omarchy requires the official-ISO VM runner: scripts/test-install-omarchy-vm.sh" ;;
  *) fail "--distro must be ubuntu, arch, or omarchy" ;;
esac
ARCHIVE="$(cd -- "$(dirname -- "${ARCHIVE}")" && pwd)/$(basename -- "${ARCHIVE}")"

RUNTIME="${SWARM_INSTALL_DISTRO_RUNTIME:-}"
if [[ -z "${RUNTIME}" ]]; then
  if command -v podman >/dev/null 2>&1; then RUNTIME=podman
  elif command -v docker >/dev/null 2>&1; then RUNTIME=docker
  else fail "podman or docker is required"
  fi
fi
command -v "${RUNTIME}" >/dev/null 2>&1 || fail "container runtime not found: ${RUNTIME}"

case "${DISTRO}" in
  ubuntu)
    IMAGE="${SWARM_INSTALL_UBUNTU_IMAGE:-ubuntu:24.04}"
    BOOTSTRAP='apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends ca-certificates curl sudo systemd'
    ;;
  arch)
    IMAGE="${SWARM_INSTALL_ARCH_IMAGE:-archlinux:base}"
    BOOTSTRAP='pacman -Syu --noconfirm --needed ca-certificates curl sudo systemd'
    ;;
esac

: "${TMPDIR:?TMPDIR must be set to disposable command scratch}"
build_root="$(mktemp -d "${TMPDIR%/}/swarm-install-image.XXXXXX")"
test_image="localhost/swarm-install-${DISTRO}-$$"
container_name="swarm-install-${DISTRO}-$$"
cleanup() {
  "${RUNTIME}" rm -f "${container_name}" >/dev/null 2>&1 || true
  if [[ -n "${container_pid:-}" ]]; then wait "${container_pid}" >/dev/null 2>&1 || true; fi
  "${RUNTIME}" image rm -f "${test_image}" >/dev/null 2>&1 || true
  rm -rf -- "${build_root}"
}
trap cleanup EXIT INT TERM
printf 'FROM %s\nRUN %s\nSTOPSIGNAL SIGRTMIN+3\nCMD ["/sbin/init"]\n' "${IMAGE}" "${BOOTSTRAP}" >"${build_root}/Containerfile"
"${RUNTIME}" build --pull -t "${test_image}" -f "${build_root}/Containerfile" "${build_root}"

run_args=(run --rm --name "${container_name}" --privileged)
if [[ "${RUNTIME}" == docker ]]; then
  run_args+=(--cgroupns=host)
fi
"${RUNTIME}" "${run_args[@]}" \
  --tmpfs /run --tmpfs /run/lock \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  -v "${ARCHIVE}:/candidate/swarm.tar.gz:ro" \
  "${test_image}" >/dev/null &
container_pid=$!

systemd_ready="false"
for _ in $(seq 1 60); do
  systemd_state="$("${RUNTIME}" exec "${container_name}" systemctl is-system-running 2>/dev/null || true)"
  case "${systemd_state}" in
    running|degraded)
      systemd_ready="true"
      break
      ;;
  esac
  sleep 1
done
[[ "${systemd_ready}" == "true" ]] || fail "systemd did not become ready in the ${DISTRO} test container"

"${RUNTIME}" exec "${container_name}" bash -se <<EOF
set -euo pipefail
command -v curl >/dev/null
useradd -m -s /bin/bash swarmtest
sudoers_dir="\$(printf '/%s/%s' etc sudoers.d)"
printf 'swarmtest ALL=(ALL) NOPASSWD:ALL\n' >"\${sudoers_dir}/swarmtest"
chmod 0440 "\${sudoers_dir}/swarmtest"
install -d -m 0755 /candidate/extract
tar -xzf /candidate/swarm.tar.gz -C /candidate/extract
artifact_root="\$(find /candidate/extract -mindepth 1 -maxdepth 1 -type d -name 'swarm-*-linux-amd64' -print -quit)"
[[ -n "\${artifact_root}" ]]
user_home="\$(getent passwd swarmtest | cut -d: -f6)"
candidate_root="\${user_home}/candidate"
install -o swarmtest -g swarmtest -d "\${candidate_root}"
cp -a "\${artifact_root}/." "\${candidate_root}/"
chown -R swarmtest:swarmtest "\${candidate_root}"
sudo -u swarmtest env -u TMPDIR HOME="\${user_home}" PATH=/usr/local/bin:/usr/bin:/bin \
  "\${candidate_root}/install.sh" --artifact-root "\${candidate_root}" --service --yes
systemctl is-active --quiet swarm.service
sudo -u swarmtest /usr/local/bin/swarm --help >/dev/null
test "\$(stat -c %u /usr/local/share/swarm)" = "\$(id -u swarmtest)"
test "\$(stat -c %g /usr/local/share/swarm)" = "\$(id -g swarmtest)"
EOF

printf 'distro=%s\nimage=%s\ninstall=passed\nservice=active\ncli=invoked\ntmpdir=unset\n' "${DISTRO}" "${IMAGE}"
