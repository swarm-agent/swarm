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
  --exclude '.tools/go' \
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

ssh "${SSH_ALIAS}" 'bash -s' -- "${REMOTE_DIR}" "${FROM_ZERO}" "${DB_PATH}" "${RESTART_SERVICE}" "${SERVICE_UNIT}" <<'REMOTE_SSH_FAST_TEST'
set -euo pipefail
remote_dir="$1"
from_zero="$2"
db_path="$3"
restart_service="$4"
service_unit="$5"

cd "${remote_dir}"
if [ "${from_zero}" = 'true' ]; then
  printf 'ssh-fast-test: deleting remote database %s\n' "${db_path}"
  if [ -e "${db_path}" ]; then
    rm -rf -- "${db_path}" 2>/dev/null || sudo -n rm -rf -- "${db_path}"
  fi
  if [ -f /etc/swarmd/swarm.conf ]; then
    printf 'ssh-fast-test: scrubbing stale managed/link config in /etc/swarmd/swarm.conf\n'
    tmp_conf="$(mktemp)"
    awk '
BEGIN {
  keys[++n] = "desktop_onboarding_complete"; repl["desktop_onboarding_complete"] = "desktop_onboarding_complete = false"
  keys[++n] = "child"; repl["child"] = "child = false"
  keys[++n] = "swarm_role"; repl["swarm_role"] = "swarm_role ="
  keys[++n] = "parent_swarm_id"; repl["parent_swarm_id"] = "parent_swarm_id ="
  keys[++n] = "pairing_state"; repl["pairing_state"] = "pairing_state ="
  keys[++n] = "managed_host_sync_mode"; repl["managed_host_sync_mode"] = "managed_host_sync_mode ="
  keys[++n] = "managed_host_sync_modules"; repl["managed_host_sync_modules"] = "managed_host_sync_modules ="
  keys[++n] = "managed_host_sync_owner_swarm_id"; repl["managed_host_sync_owner_swarm_id"] = "managed_host_sync_owner_swarm_id ="
  keys[++n] = "managed_host_sync_host_api_base_url"; repl["managed_host_sync_host_api_base_url"] = "managed_host_sync_host_api_base_url ="
  keys[++n] = "managed_host_sync_credential_url"; repl["managed_host_sync_credential_url"] = "managed_host_sync_credential_url ="
  keys[++n] = "managed_host_sync_agent_url"; repl["managed_host_sync_agent_url"] = "managed_host_sync_agent_url ="
}
{
  line = $0
  key = line
  sub(/^[[:space:]]*/, "", key)
  sub(/[[:space:]]*=.*/, "", key)
  if (key in repl) {
    print repl[key]
    seen[key] = 1
    next
  }
  print line
}
END {
  for (i = 1; i <= n; i++) {
    key = keys[i]
    if (!(key in seen)) {
      print repl[key]
    }
  }
}
' /etc/swarmd/swarm.conf >"${tmp_conf}"
    cp "${tmp_conf}" /etc/swarmd/swarm.conf 2>/dev/null || sudo -n cp "${tmp_conf}" /etc/swarmd/swarm.conf
    rm -f "${tmp_conf}"
  fi
fi
./rebuild s
if [ "${restart_service}" = 'true' ]; then
  if systemctl --user cat "${service_unit}" >/dev/null 2>&1; then
    systemctl --user daemon-reload
    systemctl --user restart "${service_unit}"
    sleep 2
    systemctl --user --no-pager --full status "${service_unit}" | sed -n '1,18p'
  else
    sudo -n systemctl daemon-reload
    sudo -n systemctl restart "${service_unit}"
    sleep 2
    sudo -n systemctl --no-pager --full status "${service_unit}" | sed -n '1,18p'
  fi
fi
REMOTE_SSH_FAST_TEST
