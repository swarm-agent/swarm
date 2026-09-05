#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/test-install-distro.sh --archive <archive.tar.gz> --checksum <archive.tar.gz.sha256> --distro <ubuntu|arch|omarchy>

Download one checksum-bound release archive inside a fresh privileged distro
environment. The runner installs only the downloader, certificate, sudo, and
systemd bootstrap needed to invoke the public installer, creates a non-root
service owner with passwordless sudo, proves the mandatory Git prerequisite is
initially missing, curls the exact candidate into that user's fresh home,
unsets TMPDIR, runs the archive's install.sh in systemd service mode, and
verifies prerequisite provisioning plus full readiness.

Environment:
  SWARM_INSTALL_DISTRO_RUNTIME  Container runtime command (default: podman, then docker)
  SWARM_INSTALL_UBUNTU_IMAGE    Ubuntu image (default: docker.io/library/ubuntu:24.04)
  SWARM_INSTALL_ARCH_IMAGE      Arch image (default: docker.io/library/archlinux:base)

Omarchy publishes a full installation ISO rather than an OCI image; use
scripts/test-install-omarchy-vm.sh against a clean official-ISO VM overlay.
EOF
}

fail() {
  printf 'test-install-distro: %s\n' "$*" >&2
  exit 1
}

ARCHIVE=""
CHECKSUM=""
DISTRO=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --archive) [[ $# -ge 2 ]] || fail "--archive requires a path"; ARCHIVE="$2"; shift 2 ;;
    --checksum) [[ $# -ge 2 ]] || fail "--checksum requires a path"; CHECKSUM="$2"; shift 2 ;;
    --distro) [[ $# -ge 2 ]] || fail "--distro requires a value"; DISTRO="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unsupported argument: $1" ;;
  esac
done

[[ -n "${ARCHIVE}" ]] || fail "--archive is required"
[[ -f "${ARCHIVE}" ]] || fail "archive does not exist: ${ARCHIVE}"
[[ -n "${CHECKSUM}" ]] || fail "--checksum is required"
[[ -f "${CHECKSUM}" ]] || fail "checksum does not exist: ${CHECKSUM}"
[[ "$(basename -- "${CHECKSUM}")" == "$(basename -- "${ARCHIVE}").sha256" ]] || fail "checksum must be named after the archive"
case "${DISTRO}" in
  ubuntu|arch) ;;
  omarchy) fail "Omarchy requires the official-ISO VM runner: scripts/test-install-omarchy-vm.sh" ;;
  *) fail "--distro must be ubuntu, arch, or omarchy" ;;
esac
ARCHIVE="$(cd -- "$(dirname -- "${ARCHIVE}")" && pwd)/$(basename -- "${ARCHIVE}")"
CHECKSUM="$(cd -- "$(dirname -- "${CHECKSUM}")" && pwd)/$(basename -- "${CHECKSUM}")"
archive_name="$(basename -- "${ARCHIVE}")"
checksum_name="$(basename -- "${CHECKSUM}")"
expected_digest="$(awk -v name="${archive_name}" '$NF == name || $NF == "*" name { print $1; exit }' "${CHECKSUM}")"
[[ "${expected_digest}" =~ ^[0-9a-fA-F]{64}$ ]] || fail "checksum does not contain an exact SHA-256 entry for ${archive_name}"

RUNTIME="${SWARM_INSTALL_DISTRO_RUNTIME:-}"
if [[ -z "${RUNTIME}" ]]; then
  if command -v podman >/dev/null 2>&1; then RUNTIME=podman
  elif command -v docker >/dev/null 2>&1; then RUNTIME=docker
  else fail "podman or docker is required"
  fi
fi
command -v "${RUNTIME}" >/dev/null 2>&1 || fail "container runtime not found: ${RUNTIME}"
command -v python3 >/dev/null 2>&1 || fail "python3 is required to serve the exact candidate over loopback HTTP"

case "${DISTRO}" in
  ubuntu)
    IMAGE="${SWARM_INSTALL_UBUNTU_IMAGE:-docker.io/library/ubuntu:24.04}"
    BOOTSTRAP='bash /bootstrap-ubuntu.sh'
    ;;
  arch)
    IMAGE="${SWARM_INSTALL_ARCH_IMAGE:-docker.io/library/archlinux:base}"
    BOOTSTRAP='pacman -Syu --noconfirm --needed ca-certificates curl sudo systemd'
    ;;
esac

: "${TMPDIR:?TMPDIR must be set to disposable command scratch}"
build_root="$(mktemp -d "${TMPDIR%/}/swarm-install-image.XXXXXX")"
test_image="localhost/swarm-install-${DISTRO}-$$"
container_name="swarm-install-${DISTRO}-$$"
cleanup() {
  if [[ -n "${server_pid:-}" ]]; then kill "${server_pid}" >/dev/null 2>&1 || true; wait "${server_pid}" >/dev/null 2>&1 || true; fi
  "${RUNTIME}" rm -f "${container_name}" >/dev/null 2>&1 || true
  if [[ -n "${container_pid:-}" ]]; then wait "${container_pid}" >/dev/null 2>&1 || true; fi
  "${RUNTIME}" image rm -f "${test_image}" >/dev/null 2>&1 || true
  rm -rf -- "${build_root}"
}
trap cleanup EXIT INT TERM
printf 'FROM %s\n' "${IMAGE}" >"${build_root}/Containerfile"
if [[ "${DISTRO}" == ubuntu ]]; then
  cp -- "$(dirname -- "${BASH_SOURCE[0]}")/install-distro-ubuntu-bootstrap.sh" "${build_root}/bootstrap-ubuntu.sh"
  printf 'COPY bootstrap-ubuntu.sh /bootstrap-ubuntu.sh\n' >>"${build_root}/Containerfile"
fi
printf 'RUN %s\nSTOPSIGNAL SIGRTMIN+3\nCMD ["/usr/lib/systemd/systemd"]\n' "${BOOTSTRAP}" >>"${build_root}/Containerfile"
"${RUNTIME}" build --pull -t "${test_image}" -f "${build_root}/Containerfile" "${build_root}"

run_args=(run --rm --name "${container_name}" --privileged)
if [[ "${RUNTIME}" == docker ]]; then
  run_args+=(--cgroupns=host --add-host=host.containers.internal:host-gateway)
fi
"${RUNTIME}" "${run_args[@]}" \
  --tmpfs /run --tmpfs /run/lock \
  -v /sys/fs/cgroup:/sys/fs/cgroup:rw \
  -v "${ARCHIVE}:/candidate-source/${archive_name}:ro" \
  -v "${CHECKSUM}:/candidate-source/${checksum_name}:ro" \
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

candidate_host="$("${RUNTIME}" exec "${container_name}" getent ahostsv4 host.containers.internal | awk 'NR == 1 { print $1 }')"
[[ "${candidate_host}" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "could not resolve the container-to-host candidate address"
port_file="${build_root}/candidate-http.port"
python3 - "${candidate_host}" "$(dirname -- "${ARCHIVE}")" "${port_file}" >"${build_root}/candidate-http.log" 2>&1 <<'PY' &
import functools
import http.server
import pathlib
import socketserver
import sys

host, directory, port_file = sys.argv[1:]
handler = functools.partial(http.server.SimpleHTTPRequestHandler, directory=directory)
with socketserver.TCPServer((host, 0), handler) as server:
    pathlib.Path(port_file).write_text(str(server.server_address[1]), encoding="ascii")
    server.serve_forever()
PY
server_pid=$!
for _ in $(seq 1 50); do
  if [[ -s "${port_file}" ]]; then server_ready=true; break; fi
  sleep 0.1
done
kill -0 "${server_pid}" 2>/dev/null || fail "candidate download server exited before the test"
[[ "${server_ready:-false}" == true ]] || fail "candidate download server did not become ready"
candidate_port="$(cat "${port_file}")"
[[ "${candidate_port}" =~ ^[0-9]+$ ]] || fail "candidate download server selected an invalid port"

"${RUNTIME}" exec "${container_name}" bash -se <<'EOF'
set -euo pipefail
if command -v git >/dev/null 2>&1; then
  echo "test image unexpectedly satisfies the Git provisioning precondition" >&2
  exit 1
fi
EOF

"${RUNTIME}" exec "${container_name}" bash -se <<EOF
set -euo pipefail
command -v curl >/dev/null
useradd -m -s /bin/bash swarmtest
sudoers_dir="\$(printf '/%s/%s' etc sudoers.d)"
printf 'swarmtest ALL=(ALL) NOPASSWD:ALL\n' >"\${sudoers_dir}/swarmtest"
chmod 0440 "\${sudoers_dir}/swarmtest"
user_home="\$(getent passwd swarmtest | cut -d: -f6)"
download_root="\${user_home}/Downloads/swarm-candidate"
install -o swarmtest -g swarmtest -d "\${user_home}/Downloads" "\${download_root}"
sudo -u swarmtest curl -fsSL "http://host.containers.internal:${candidate_port}/${archive_name}" -o "\${download_root}/${archive_name}"
sudo -u swarmtest curl -fsSL "http://host.containers.internal:${candidate_port}/${checksum_name}" -o "\${download_root}/${checksum_name}"
(
  cd "\${download_root}"
  sudo -u swarmtest sha256sum -c "${checksum_name}"
)
test "\$(sha256sum "\${download_root}/${archive_name}" | awk '{print \$1}')" = "${expected_digest}"
install -o swarmtest -g swarmtest -d "\${download_root}/extract"
sudo -u swarmtest tar -xzf "\${download_root}/${archive_name}" -C "\${download_root}/extract"
artifact_root="\$(find "\${download_root}/extract" -mindepth 1 -maxdepth 1 -type d -name 'swarm-*-linux-amd64' -print -quit)"
[[ -n "\${artifact_root}" ]]
sudo -u swarmtest env -u TMPDIR HOME="\${user_home}" PATH=/usr/local/bin:/usr/bin:/bin \
  "\${artifact_root}/install.sh" --artifact-root "\${artifact_root}" --service --yes
command -v git >/dev/null
sudo -u swarmtest git --version >/dev/null
systemctl is-active --quiet swarm.service
status_output="\$(sudo -u swarmtest /usr/local/bin/swarm status)"
grep -Fxq 'active=active' <<<"\${status_output}"
grep -Fxq 'daemon_status=running' <<<"\${status_output}"
grep -Fxq 'daemon_health=healthy' <<<"\${status_output}"
sudo -u swarmtest /usr/local/bin/swarm --help >/dev/null
test "\$(stat -c %u /usr/local/share/swarm)" = "\$(id -u swarmtest)"
test "\$(stat -c %g /usr/local/share/swarm)" = "\$(id -g swarmtest)"
EOF

printf 'distro=%s\nimage=%s\ncandidate_archive=%s\ncandidate_sha256=%s\ncandidate_download=passed\nmandatory_git_precondition=missing\nmandatory_git_provisioning=passed\ninstall=passed\nservice=active\ndaemon_readiness=healthy\ncli=invoked\ntmpdir=unset\n' "${DISTRO}" "${IMAGE}" "${archive_name}" "${expected_digest}"
