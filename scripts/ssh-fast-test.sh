#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/ssh-fast-test.sh <ssh-alias> [--branch <name>] [--remote-dir <path>] [--service <unit>] [--no-restart]
       scripts/ssh-fast-test.sh <ssh-alias> --from-zero [--branch <name>] [--remote-dir <path>] [--service <unit>] [--db-path <path>]

Fast SSH testing flow:
  1. require the local git worktree to be clean so only committed changes sync
  2. find the remote swarm-go checkout unless --remote-dir is provided
  3. create a git bundle for the selected local branch or HEAD and copy it over SSH
  4. fetch/reset the remote checkout to the bundled commit and clean untracked files
  5. run the checked-in rebuild script from the remote checkout
  6. restart the remote user systemd service unless --no-restart is set

Rebuild-from-zero flow:
  1. run the same committed git-bundle transport as the fast flow
  2. stop the remote user systemd service before updating the checkout
  3. delete the remote Pebble database path
  4. run the checked-in rebuild script from the remote checkout
  5. restart the remote user systemd service

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
  local value="$1"
  printf "'"
  printf '%s' "${value}" | sed "s/'/'\\\\''/g"
  printf "'"
}

require_clean_git_tree() {
  git rev-parse --is-inside-work-tree >/dev/null 2>&1 || fail "requires a git checkout"
  if ! git diff --quiet --ignore-submodules -- || ! git diff --cached --quiet --ignore-submodules --; then
    fail "requires committed changes only; commit or stash local modifications first"
  fi
  if [[ -n "$(git ls-files --others --exclude-standard)" ]]; then
    fail "requires committed changes only; commit, stash, or remove untracked files first"
  fi
}

create_ref_bundle() {
  local bundle_path="$1"
  local git_ref="$2"
  git bundle create "${bundle_path}" "${git_ref}" >/dev/null
  git bundle verify "${bundle_path}" >/dev/null
}

require_local_branch() {
  local branch_name="$1"
  [[ -n "${branch_name}" ]] || fail "--branch requires a non-empty value"
  git check-ref-format --branch "${branch_name}" >/dev/null || fail "invalid branch name: ${branch_name}"
  git show-ref --verify --quiet "refs/heads/${branch_name}" || fail "local branch not found: ${branch_name}"
}

copy_bundle_to_remote() {
  local bundle_path="$1"
  local remote_bundle_path="$2"
  local remote_bundle_quoted
  remote_bundle_quoted="$(quote_remote "${remote_bundle_path}")"
  ssh "${SSH_ALIAS}" "bash -c 'set -euo pipefail; remote_bundle_path=\"\$1\"; mkdir -p -- \"\$(dirname -- \"\${remote_bundle_path}\")\"; cat > \"\${remote_bundle_path}\"' bash ${remote_bundle_quoted}" <"${bundle_path}"
}

cleanup_remote_bundle() {
  local remote_bundle_path="$1"
  ssh "${SSH_ALIAS}" 'bash -s' -- "${remote_bundle_path}" <<'REMOTE_CLEAN_BUNDLE'
set -euo pipefail
rm -f -- "$1"
REMOTE_CLEAN_BUNDLE
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
TARGET_BRANCH=""

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
    --branch)
      [[ $# -ge 2 ]] || fail "--branch requires a value"
      TARGET_BRANCH="$2"
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
require_command git

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

require_clean_git_tree
if [[ -n "${TARGET_BRANCH}" ]]; then
  require_local_branch "${TARGET_BRANCH}"
  LOCAL_REF="refs/heads/${TARGET_BRANCH}"
  LOCAL_HEAD="$(git rev-parse --verify "${LOCAL_REF}^{commit}")"
  LOCAL_BRANCH="${TARGET_BRANCH}"
else
  LOCAL_REF="HEAD"
  LOCAL_HEAD="$(git rev-parse --verify HEAD)"
  LOCAL_BRANCH="$(git branch --show-current 2>/dev/null || true)"
fi
LOCAL_BUNDLE="$(mktemp "${TMPDIR:-/tmp}/swarm-fast-test-${LOCAL_HEAD:0:12}-XXXXXX.bundle")"
trap 'rm -f -- "${LOCAL_BUNDLE}"' EXIT
create_ref_bundle "${LOCAL_BUNDLE}" "${LOCAL_REF}"

if [[ -z "${REMOTE_DIR}" ]]; then
  REMOTE_DIR="$(ssh "${SSH_ALIAS}" 'bash -s' 2>/dev/null <<'REMOTE_DISCOVER_CHECKOUT'
set -euo pipefail
for candidate in "$HOME/swarm-go" "$HOME/src/swarm-go" "$HOME/work/swarm-go"; do
  if [ -d "$candidate/.git" ] && [ -f "$candidate/AGENTS.md" ] && [ -x "$candidate/rebuild" ]; then
    printf "%s\n" "$candidate"
    exit 0
  fi
done
find "$HOME" /opt /srv /tmp -maxdepth 4 -type d -name swarm-go 2>/dev/null \
  | while IFS= read -r candidate; do
      if [ -d "$candidate/.git" ] && [ -f "$candidate/AGENTS.md" ] && [ -x "$candidate/rebuild" ]; then
        printf "%s\n" "$candidate"
        exit 0
      fi
    done
exit 1
REMOTE_DISCOVER_CHECKOUT
)" || fail "could not find remote git checkout on ${SSH_ALIAS}; pass --remote-dir"
fi

[[ -n "${REMOTE_DIR}" ]] || fail "empty remote checkout path"
[[ -n "${DB_PATH}" ]] || fail "empty database path"

REMOTE_BUNDLE_DIR="${SWARM_SSH_FAST_TEST_REMOTE_TMPDIR:-${TMPDIR:-/tmp}}"
REMOTE_BUNDLE_PATH="${REMOTE_BUNDLE_DIR%/}/swarm-fast-test-${LOCAL_HEAD:0:12}-$$.bundle"
printf 'ssh-fast-test: remote=%s dir=%s service=%s mode=%s commit=%s branch=%s\n' "${SSH_ALIAS}" "${REMOTE_DIR}" "${SERVICE_UNIT}" "$([[ "${FROM_ZERO}" == "true" ]] && printf from-zero || printf fast)" "${LOCAL_HEAD}" "${LOCAL_BRANCH:-detached}"
printf 'ssh-fast-test: copying git bundle to %s:%s\n' "${SSH_ALIAS}" "${REMOTE_BUNDLE_PATH}"
copy_bundle_to_remote "${LOCAL_BUNDLE}" "${REMOTE_BUNDLE_PATH}"
trap 'rm -f -- "${LOCAL_BUNDLE}"; cleanup_remote_bundle "${REMOTE_BUNDLE_PATH}" >/dev/null 2>&1 || true' EXIT

if [[ "${FROM_ZERO}" == "true" ]]; then
  printf 'ssh-fast-test: stopping remote service before checkout update\n'
  ssh "${SSH_ALIAS}" 'bash -s' -- "${SERVICE_UNIT}" <<'REMOTE_STOP_SERVICE'
set -euo pipefail
service_unit="$1"
systemctl --user stop "${service_unit}" >/dev/null 2>&1 || true
if command -v sudo >/dev/null 2>&1; then
  sudo -n systemctl stop "${service_unit}" >/dev/null 2>&1 || true
fi
REMOTE_STOP_SERVICE
fi

ssh "${SSH_ALIAS}" 'bash -s' -- "${REMOTE_DIR}" "${FROM_ZERO}" "${DB_PATH}" "${RESTART_SERVICE}" "${SERVICE_UNIT}" "${REMOTE_BUNDLE_PATH}" "${LOCAL_HEAD}" "${LOCAL_BRANCH}" "${LOCAL_REF}" <<'REMOTE_SSH_FAST_TEST'
set -euo pipefail
remote_dir="$1"
from_zero="$2"
db_path="$3"
restart_service="$4"
service_unit="$5"
remote_bundle_path="$6"
local_head="$7"
local_branch="$8"
bundle_ref="$9"

cd "${remote_dir}"
if [ ! -d .git ]; then
  printf 'ssh-fast-test: remote checkout %s is not a git repository\n' "${remote_dir}" >&2
  exit 1
fi
printf 'ssh-fast-test: fetching bundled ref %s commit %s\n' "${bundle_ref}" "${local_head}"
git bundle verify "${remote_bundle_path}" >/dev/null
git fetch --force "${remote_bundle_path}" "${bundle_ref}"
git reset --hard
git clean -fdx -e .tools/go/ -e .cache/ -e web/node_modules/ -e node_modules/
if [ -n "${local_branch}" ]; then
  git checkout -B "${local_branch}" FETCH_HEAD
else
  git checkout --detach FETCH_HEAD
fi
git reset --hard FETCH_HEAD
git clean -fdx -e .tools/go/ -e .cache/ -e web/node_modules/ -e node_modules/
current_head="$(git rev-parse --verify HEAD)"
if [ "${current_head}" != "${local_head}" ]; then
  printf 'ssh-fast-test: remote HEAD %s != bundled HEAD %s\n' "${current_head}" "${local_head}" >&2
  exit 1
fi
printf 'ssh-fast-test: remote checkout now at %s\n' "${current_head}"
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
if [ ! -f web/node_modules/vite/bin/vite.js ]; then
  printf 'ssh-fast-test: installing remote web dependencies\n'
  if ! command -v pnpm >/dev/null 2>&1; then
    printf 'ssh-fast-test: pnpm is required to install web dependencies\n' >&2
    exit 1
  fi
  (cd web && pnpm install --frozen-lockfile)
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
