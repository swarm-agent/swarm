#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/ssh-fast-test.sh <ssh-alias> [--remote-dir <path>] [--service <unit>] [--no-restart]

Fast SSH testing flow:
  1. find the remote swarm-go checkout unless --remote-dir is provided
  2. rsync the current working tree there, excluding local artifacts
  3. run the checked-in rebuild script from the remote checkout
  4. restart the remote user systemd service unless --no-restart is set

The SSH alias, remote checkout path, and service unit are runtime inputs; do not
hardcode host-specific values in this script.
USAGE
}

fail() {
  printf 'ssh-fast-test: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

quote_remote() {
  printf '%q' "$1"
}

if [[ $# -lt 1 ]]; then
  usage
  exit 2
fi

SSH_ALIAS="$1"
shift
REMOTE_DIR=""
SERVICE_UNIT="swarm.service"
RESTART_SERVICE="true"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --remote-dir)
      [[ $# -ge 2 ]] || fail "--remote-dir requires a value"
      REMOTE_DIR="$2"
      shift 2
      ;;
    --service)
      [[ $# -ge 2 ]] || fail "--service requires a value"
      SERVICE_UNIT="$2"
      shift 2
      ;;
    --no-restart)
      RESTART_SERVICE="false"
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "unknown argument: $1"
      ;;
  esac
done

require_command ssh
require_command rsync

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

if [[ -z "${REMOTE_DIR}" ]]; then
  REMOTE_DIR="$(ssh "${SSH_ALIAS}" 'set -euo pipefail
for candidate in "$HOME/swarm-go" "$HOME/src/swarm-go" "$HOME/work/swarm-go"; do
  if [ -d "$candidate" ] && [ -f "$candidate/AGENTS.md" ] && [ -x "$candidate/rebuild" ]; then
    printf "%s\n" "$candidate"
    exit 0
  fi
done
find "$HOME" /opt /srv /tmp -maxdepth 4 -type d -name swarm-go 2>/dev/null \
  | while IFS= read -r candidate; do
      if [ -f "$candidate/AGENTS.md" ] && [ -x "$candidate/rebuild" ]; then
        printf "%s\n" "$candidate"
        exit 0
      fi
    done
exit 1' 2>/dev/null)" || fail "could not find remote swarm-go checkout on ${SSH_ALIAS}; pass --remote-dir"
fi

[[ -n "${REMOTE_DIR}" ]] || fail "empty remote checkout path"

printf 'ssh-fast-test: remote=%s dir=%s service=%s\n' "${SSH_ALIAS}" "${REMOTE_DIR}" "${SERVICE_UNIT}"

rsync -az --delete \
  --exclude '.git/' \
  --exclude '.cache/' \
  --exclude '.tmp/' \
  --exclude 'tmp/' \
  --exclude '.swarm/' \
  --exclude '.swarm-prof/' \
  --exclude 'dist/' \
  --exclude 'web/dist/' \
  --exclude 'web/node_modules/' \
  --exclude 'node_modules/' \
  --exclude 'bin/' \
  --exclude '.bin/' \
  --exclude 'swarm' \
  --exclude 'swarmtui' \
  --exclude 'swarmd/swarmd' \
  ./ "${SSH_ALIAS}:${REMOTE_DIR}/"

remote_dir_quoted="$(quote_remote "${REMOTE_DIR}")"
service_quoted="$(quote_remote "${SERVICE_UNIT}")"

ssh "${SSH_ALIAS}" "set -euo pipefail
cd ${remote_dir_quoted}
./rebuild s
if [ ${RESTART_SERVICE@Q} = 'true' ]; then
  systemctl --user daemon-reload
  systemctl --user restart ${service_quoted}
  sleep 2
  systemctl --user --no-pager --full status ${service_quoted} | sed -n '1,18p'
fi"
