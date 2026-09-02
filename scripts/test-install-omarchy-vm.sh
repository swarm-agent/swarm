#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'EOF'
Usage:
  ./scripts/test-install-omarchy-vm.sh --archive <archive.tar.gz> --guest <user@host> [--port <n>] [--identity <path>]

Run the same fresh-install assertion on an already-provisioned Omarchy VM. The
VM must come from the official Omarchy ISO unattended-install path and expose
SSH to a non-root user with passwordless sudo. This runner installs curl first,
copies the exact candidate archive over SSH, unsets TMPDIR for install.sh,
starts the systemd service, invokes the installed CLI, and verifies ownership.

The repository does not download or embed the multi-gigabyte Omarchy ISO in
GitHub Actions. On testbench, create a reusable clean base once with Omarchy's
official cidata unattended flow, then boot a throwaway overlay for this runner.
EOF
}

fail() { printf 'test-install-omarchy-vm: %s\n' "$*" >&2; exit 1; }

ARCHIVE=""
GUEST=""
PORT=""
IDENTITY=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --archive) [[ $# -ge 2 ]] || fail "--archive requires a path"; ARCHIVE="$2"; shift 2 ;;
    --guest) [[ $# -ge 2 ]] || fail "--guest requires user@host"; GUEST="$2"; shift 2 ;;
    --port) [[ $# -ge 2 ]] || fail "--port requires a value"; PORT="$2"; shift 2 ;;
    --identity) [[ $# -ge 2 ]] || fail "--identity requires a path"; IDENTITY="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unsupported argument: $1" ;;
  esac
done

[[ -f "${ARCHIVE}" ]] || fail "archive does not exist: ${ARCHIVE}"
[[ "${GUEST}" == *@* ]] || fail "--guest must be user@host"
[[ -z "${PORT}" || "${PORT}" =~ ^[0-9]+$ ]] || fail "--port must be numeric"
[[ -z "${IDENTITY}" || -f "${IDENTITY}" ]] || fail "identity file does not exist: ${IDENTITY}"
ARCHIVE="$(cd -- "$(dirname -- "${ARCHIVE}")" && pwd)/$(basename -- "${ARCHIVE}")"

ssh_args=(-o BatchMode=yes -o StrictHostKeyChecking=accept-new)
[[ -z "${PORT}" ]] || ssh_args+=(-p "${PORT}")
[[ -z "${IDENTITY}" ]] || ssh_args+=(-i "${IDENTITY}")
scp_args=(-q -o BatchMode=yes -o StrictHostKeyChecking=accept-new)
[[ -z "${PORT}" ]] || scp_args+=(-P "${PORT}")
[[ -z "${IDENTITY}" ]] || scp_args+=(-i "${IDENTITY}")

remote_archive="swarm-candidate.tar.gz"
scp "${scp_args[@]}" "${ARCHIVE}" "${GUEST}:${remote_archive}"
ssh "${ssh_args[@]}" "${GUEST}" bash -se -- "${remote_archive}" <<'EOF'
set -euo pipefail
archive="$1"
sudo pacman -Syu --noconfirm --needed ca-certificates curl
work="$(mktemp -d)"
trap 'rm -rf -- "$work" "$archive"' EXIT
tar -xzf "$archive" -C "$work"
artifact_root="$(find "$work" -mindepth 1 -maxdepth 1 -type d -name 'swarm-*-linux-amd64' -print -quit)"
[[ -n "$artifact_root" ]]
env -u TMPDIR "$artifact_root/install.sh" --artifact-root "$artifact_root" --service --yes
systemctl is-active --quiet swarm.service
/usr/local/bin/swarm --help >/dev/null
test "$(stat -c %u /usr/local/share/swarm)" = "$(id -u)"
test "$(stat -c %g /usr/local/share/swarm)" = "$(id -g)"
EOF

printf 'distro=omarchy\ninstall=passed\nservice=active\ncli=invoked\ntmpdir=unset\n'
