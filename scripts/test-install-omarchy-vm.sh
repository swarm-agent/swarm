#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/test-install-omarchy-vm.sh --archive <archive.tar.gz> --checksum <archive.tar.gz.sha256> --guest <user@host> [--port <n>] [--identity <path>]

Run the same fresh-install assertion on an already-provisioned Omarchy VM. The
VM must come from the official Omarchy ISO unattended-install path and expose
SSH to a non-root user with passwordless sudo. This runner removes Git after
fetching the installer input, copies the exact checksum-bound candidate over
SSH, unsets TMPDIR for install.sh, and verifies Git provisioning plus full readiness.

The repository does not download or embed the multi-gigabyte Omarchy ISO in
GitHub Actions. On testbench, create a reusable clean base once with Omarchy's
official cidata unattended flow, then boot a throwaway overlay for this runner.
EOF
}

fail() { printf 'test-install-omarchy-vm: %s\n' "$*" >&2; exit 1; }

ARCHIVE=""
CHECKSUM=""
GUEST=""
PORT=""
IDENTITY=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --archive) [[ $# -ge 2 ]] || fail "--archive requires a path"; ARCHIVE="$2"; shift 2 ;;
    --checksum) [[ $# -ge 2 ]] || fail "--checksum requires a path"; CHECKSUM="$2"; shift 2 ;;
    --guest) [[ $# -ge 2 ]] || fail "--guest requires user@host"; GUEST="$2"; shift 2 ;;
    --port) [[ $# -ge 2 ]] || fail "--port requires a value"; PORT="$2"; shift 2 ;;
    --identity) [[ $# -ge 2 ]] || fail "--identity requires a path"; IDENTITY="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unsupported argument: $1" ;;
  esac
done

[[ -f "${ARCHIVE}" ]] || fail "archive does not exist: ${ARCHIVE}"
[[ -f "${CHECKSUM}" ]] || fail "checksum does not exist: ${CHECKSUM}"
[[ "$(basename -- "${CHECKSUM}")" == "$(basename -- "${ARCHIVE}").sha256" ]] || fail "checksum must be named after the archive"
[[ "${GUEST}" == *@* ]] || fail "--guest must be user@host"
[[ -z "${PORT}" || "${PORT}" =~ ^[0-9]+$ ]] || fail "--port must be numeric"
[[ -z "${IDENTITY}" || -f "${IDENTITY}" ]] || fail "identity file does not exist: ${IDENTITY}"
ARCHIVE="$(cd -- "$(dirname -- "${ARCHIVE}")" && pwd)/$(basename -- "${ARCHIVE}")"
CHECKSUM="$(cd -- "$(dirname -- "${CHECKSUM}")" && pwd)/$(basename -- "${CHECKSUM}")"

ssh_args=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new)
[[ -z "${PORT}" ]] || ssh_args+=(-p "${PORT}")
[[ -z "${IDENTITY}" ]] || ssh_args+=(-i "${IDENTITY}")
scp_args=(-q -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
[[ -z "${PORT}" ]] || scp_args+=(-P "${PORT}")
[[ -z "${IDENTITY}" ]] || scp_args+=(-i "${IDENTITY}")

archive_name="$(basename -- "${ARCHIVE}")"
checksum_name="$(basename -- "${CHECKSUM}")"
remote_root="swarm-candidate-download"
ssh "${ssh_args[@]}" "${GUEST}" "rm -rf -- '${remote_root}' && mkdir -m 0700 -- '${remote_root}'"
scp "${scp_args[@]}" "${ARCHIVE}" "${CHECKSUM}" "${GUEST}:${remote_root}/"
ssh "${ssh_args[@]}" "${GUEST}" bash -se -- "${remote_root}/${archive_name}" "${remote_root}/${checksum_name}" <<'EOF'
set -euo pipefail
archive="$1"
checksum="$2"
sudo pacman -Syu --noconfirm --needed ca-certificates curl
sudo pacman -Rdd --noconfirm git >/dev/null 2>&1 || true
if command -v git >/dev/null 2>&1; then
  echo "Omarchy guest still has Git before Swarm installation" >&2
  exit 1
fi
work="$(mktemp -d)"
trap 'rm -rf -- "$work" "$(dirname -- "$archive")"' EXIT
(
  cd "$(dirname -- "$archive")"
  sha256sum -c "$(basename -- "$checksum")"
)
tar -xzf "$archive" -C "$work"
artifact_root="$(find "$work" -mindepth 1 -maxdepth 1 -type d -name 'swarm-*-linux-amd64' -print -quit)"
[[ -n "$artifact_root" ]]
env -u TMPDIR "$artifact_root/install.sh" --artifact-root "$artifact_root" --service --yes
command -v git >/dev/null
git --version >/dev/null
systemctl is-active --quiet swarm.service
status_output="$(/usr/local/bin/swarm status)"
grep -Fxq 'active=active' <<<"$status_output"
grep -Fxq 'daemon_status=running' <<<"$status_output"
grep -Fxq 'daemon_health=healthy' <<<"$status_output"
/usr/local/bin/swarm --help >/dev/null
test "$(stat -c %u /usr/local/share/swarm)" = "$(id -u)"
test "$(stat -c %g /usr/local/share/swarm)" = "$(id -g)"
EOF

printf 'distro=omarchy\ncandidate_download=passed\ngit_preinstall=absent\ngit_installer_provisioned=passed\ninstall=passed\nservice=active\ndaemon_readiness=healthy\ncli=invoked\ntmpdir=unset\n'
