#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/ssh-fast-test.sh <ssh-alias> [--remote-dir <path>] [--service <unit>] [--no-restart]
       scripts/ssh-fast-test.sh <ssh-alias> --from-zero [--remote-dir <path>] [--service <unit>] [--db-path <path>]

Fast SSH testing flow:
  1. find the remote swarm-go checkout unless --remote-dir is provided
  2. rsync the current working tree there, excluding local artifacts
  3. run the checked-in rebuild script from the remote checkout
  4. restart the remote user systemd service unless --no-restart is set

Rebuild-from-zero flow:
  1. require the local git worktree to be clean so only committed changes sync
  2. stop the remote user systemd service before rsync
  3. rsync the committed working tree to the remote checkout
  4. delete the remote Pebble database path
  5. run the checked-in rebuild script from the remote checkout
  6. restart the remote user systemd service

The SSH alias, remote checkout path, service unit, and database path are runtime
inputs; do not hardcode host-specific values in this script.
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

require_clean_git_tree() {
  git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail "--from-zero requires a git checkout"
  if ! git diff --quiet -- || ! git diff --cached --quiet --; then
    fail "--from-zero requires committed changes only; commit or stash local modifications first"
  fi
  if [[ -n "$(git ls-files --others --exclude-standard)" ]]; then
    fail "--from-zero requires committed changes only; commit, stash, or remove untracked files first"
  fi
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
FROM_ZERO="false"
DB_PATH="/var/lib/swarmd/swarmd.pebble"

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
    --db-path)
      [[ $# -ge 2 ]] || fail "--db-path requires a value"
      DB_PATH="$2"
      shift 2
      ;;
    --from-zero|--rebuild-from-zero)
      FROM_ZERO="true"
      shift
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

if [[ "${FROM_ZERO}" == "true" && "${RESTART_SERVICE}" != "true" ]]; then
  fail "--from-zero always restarts the service; remove --no-restart"
fi

require_command ssh
require_command rsync

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

if [[ "${FROM_ZERO}" == "true" ]]; then
  require_clean_git_tree
fi

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
[[ -n "${DB_PATH}" ]] || fail "empty database path"

remote_dir_quoted="$(quote_remote "${REMOTE_DIR}")"
service_quoted="$(quote_remote "${SERVICE_UNIT}")"
db_path_quoted="$(quote_remote "${DB_PATH}")"

printf 'ssh-fast-test: remote=%s dir=%s service=%s mode=%s\n' "${SSH_ALIAS}" "${REMOTE_DIR}" "${SERVICE_UNIT}" "$([[ "${FROM_ZERO}" == "true" ]] && printf from-zero || printf fast)"

if [[ "${FROM_ZERO}" == "true" ]]; then
  printf 'ssh-fast-test: stopping remote service before sync\n'
  ssh "${SSH_ALIAS}" "set -euo pipefail
systemctl --user stop ${service_quoted} >/dev/null 2>&1 || true
if command -v sudo >/dev/null 2>&1; then
  sudo -n systemctl stop ${service_quoted} >/dev/null 2>&1 || true
fi"
fi

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
  --exclude '/swarm' \
  --exclude '/swarmtui' \
  --exclude '/swarmd/swarmd' \
  ./ "${SSH_ALIAS}:${REMOTE_DIR}/"

ssh "${SSH_ALIAS}" "set -euo pipefail
cd ${remote_dir_quoted}
if [ ${FROM_ZERO@Q} = 'true' ]; then
  printf 'ssh-fast-test: deleting remote database %s\\n' ${db_path_quoted}
  if [ -e ${db_path_quoted} ]; then
    rm -rf -- ${db_path_quoted} 2>/dev/null || sudo -n rm -rf -- ${db_path_quoted}
  fi
  if [ -f /etc/swarmd/swarm.conf ]; then
    printf 'ssh-fast-test: resetting desktop_onboarding_complete in /etc/swarmd/swarm.conf\\n'
    if grep -q '^[[:space:]]*desktop_onboarding_complete[[:space:]]*=' /etc/swarmd/swarm.conf; then
      sed -i 's/^[[:space:]]*desktop_onboarding_complete[[:space:]]*=.*/desktop_onboarding_complete = false/' /etc/swarmd/swarm.conf 2>/dev/null || sudo -n sed -i 's/^[[:space:]]*desktop_onboarding_complete[[:space:]]*=.*/desktop_onboarding_complete = false/' /etc/swarmd/swarm.conf
    else
      printf '\\ndesktop_onboarding_complete = false\\n' >>/etc/swarmd/swarm.conf 2>/dev/null || printf '\\ndesktop_onboarding_complete = false\\n' | sudo -n tee -a /etc/swarmd/swarm.conf >/dev/null
    fi
  fi
fi
./rebuild s
if [ ${RESTART_SERVICE@Q} = 'true' ]; then
  systemctl --user daemon-reload
  systemctl --user restart ${service_quoted}
  sleep 2
  systemctl --user --no-pager --full status ${service_quoted} | sed -n '1,18p'
fi"
