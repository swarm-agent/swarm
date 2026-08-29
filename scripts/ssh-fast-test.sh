#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat >&2 <<'USAGE'
Usage: scripts/ssh-fast-test.sh <ssh-alias> [--branch <name>] [--remote-dir <path>] [--deploy-dir <path>] [--prepare-only] [--service <unit>] [--no-restart] [--allow-dirty-committed-ref]
       scripts/ssh-fast-test.sh <ssh-alias> --from-zero [--branch <name>] [--remote-dir <path>] [--deploy-dir <path>] [--service <unit>] [--db-path <path>] [--allow-dirty-committed-ref]

Fast SSH testing flow:
  1. require the local git worktree to be clean so only committed changes sync
  2. find the remote user workspace checkout unless --remote-dir is provided
  3. create a git bundle for the selected local branch or HEAD and copy it over SSH
  4. fetch the bundled commit without switching or resetting the user workspace checkout
  5. create/update a detached deployment worktree and run its checked-in rebuild script
  6. restart the remote user systemd service unless --no-restart is set

Rebuild-from-zero flow:
  1. run the same committed git-bundle transport as the fast flow
  2. stop the remote user systemd service before updating the checkout
  3. delete the remote Pebble database path
  4. run the checked-in rebuild script from the remote checkout
  5. restart the remote user systemd service

The SSH alias, remote user workspace checkout, optional detached deployment
worktree, service unit, and database path are runtime inputs; do not hardcode
host-specific values in this script. --prepare-only stops after safely updating
the detached deployment worktree and never rebuilds or restarts. The
--allow-dirty-committed-ref option is for testbench wrappers that deliberately
bundle committed HEAD while preserving unrelated local modifications.
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

remote_service_action() {
  local action="$1"
  ssh "${SSH_ALIAS}" 'bash -s' -- "${SERVICE_UNIT}" "${action}" <<'REMOTE_SERVICE_ACTION'
set -euo pipefail
service_unit="$1"
action="$2"
admin_broker='/usr/local/sbin/swarm-testbench-admin'
if systemctl --user cat "${service_unit}" >/dev/null 2>&1; then
  case "${action}" in
    status) systemctl --user --no-pager --full status "${service_unit}" | sed -n '1,18p' ;;
    stop) systemctl --user stop "${service_unit}" ;;
    reload) systemctl --user daemon-reload ;;
    restart) systemctl --user restart "${service_unit}" ;;
    *) printf 'ssh-fast-test: unsupported service action: %s\n' "${action}" >&2; exit 2 ;;
  esac
  exit 0
fi
if [ "${service_unit}" != 'swarm.service' ]; then
  printf 'ssh-fast-test: system service must use the fixed swarm.service unit\n' >&2
  exit 1
fi
if [ ! -x "${admin_broker}" ]; then
  printf 'ssh-fast-test: fixed testbench administration broker is unavailable\n' >&2
  exit 1
fi
case "${action}" in
  status|stop|reload|restart) sudo -n "${admin_broker}" "swarm-service-${action}" ;;
  *) printf 'ssh-fast-test: unsupported service action: %s\n' "${action}" >&2; exit 2 ;;
esac
REMOTE_SERVICE_ACTION
}

if [[ $# -lt 1 ]]; then
  usage
  exit 2
fi

SSH_ALIAS="$1"
shift
REMOTE_DIR=""
REMOTE_DEPLOY_DIR=""
SERVICE_UNIT="swarm.service"
RESTART_SERVICE="true"
FROM_ZERO="false"
PREPARE_ONLY="false"
DB_PATH="/var/lib/swarmd/swarmd.pebble"
TARGET_BRANCH=""
ALLOW_DIRTY_COMMITTED_REF="false"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --remote-dir)
      [[ $# -ge 2 ]] || fail "--remote-dir requires a value"
      REMOTE_DIR="$2"
      shift 2
      ;;
    --deploy-dir)
      [[ $# -ge 2 ]] || fail "--deploy-dir requires a value"
      REMOTE_DEPLOY_DIR="$2"
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
    --prepare-only)
      PREPARE_ONLY="true"
      RESTART_SERVICE="false"
      shift
      ;;
    --no-restart)
      RESTART_SERVICE="false"
      shift
      ;;
    --allow-dirty-committed-ref)
      ALLOW_DIRTY_COMMITTED_REF="true"
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

if [[ "${FROM_ZERO}" == "true" && "${PREPARE_ONLY}" == "true" ]]; then
  fail "--from-zero cannot be combined with --prepare-only"
fi
if [[ "${FROM_ZERO}" == "true" && "${RESTART_SERVICE}" != "true" ]]; then
  fail "--from-zero always restarts the service; remove --no-restart"
fi

require_command ssh
require_command git

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

if [[ "${ALLOW_DIRTY_COMMITTED_REF}" != "true" ]]; then
  require_clean_git_tree
fi
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
printf 'ssh-fast-test: remote=%s source_dir=%s deploy_dir=%s service=%s mode=%s commit=%s branch=%s\n' "${SSH_ALIAS}" "${REMOTE_DIR}" "${REMOTE_DEPLOY_DIR:-auto}" "${SERVICE_UNIT}" "$([[ "${FROM_ZERO}" == "true" ]] && printf from-zero || printf fast)" "${LOCAL_HEAD}" "${LOCAL_BRANCH:-detached}"
printf 'ssh-fast-test: copying git bundle to %s:%s\n' "${SSH_ALIAS}" "${REMOTE_BUNDLE_PATH}"
copy_bundle_to_remote "${LOCAL_BUNDLE}" "${REMOTE_BUNDLE_PATH}"
trap 'rm -f -- "${LOCAL_BUNDLE}"; cleanup_remote_bundle "${REMOTE_BUNDLE_PATH}" >/dev/null 2>&1 || true' EXIT

if [[ "${FROM_ZERO}" == "true" ]]; then
  printf 'ssh-fast-test: stopping remote service before checkout update\n'
  remote_service_action stop
fi

remote_command="bash -s --"
for remote_arg in "${REMOTE_DIR}" "${REMOTE_DEPLOY_DIR}" "${FROM_ZERO}" "${PREPARE_ONLY}" "${DB_PATH}" "${RESTART_SERVICE}" "${SERVICE_UNIT}" "${REMOTE_BUNDLE_PATH}" "${LOCAL_HEAD}" "${LOCAL_BRANCH}" "${LOCAL_REF}"; do
  remote_command+=" $(quote_remote "${remote_arg}")"
done
ssh "${SSH_ALIAS}" "${remote_command}" <<'REMOTE_SSH_FAST_TEST'
set -euo pipefail
source_dir="$1"
deploy_dir="$2"
from_zero="$3"
prepare_only="$4"
db_path="$5"
restart_service="$6"
service_unit="$7"
remote_bundle_path="$8"
local_head="$9"
local_branch="${10}"
bundle_ref="${11}"

cd "${source_dir}"
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  printf 'ssh-fast-test: remote source workspace %s is not a git repository\n' "${source_dir}" >&2
  exit 1
fi
source_head_before="$(git rev-parse --verify HEAD)"
source_branch_before="$(git branch --show-current 2>/dev/null || true)"
if [ -z "${deploy_dir}" ]; then
  repo_name="$(basename -- "${source_dir}")"
  source_digest="$(printf '%s' "${source_dir}" | sha256sum | cut -c1-12)"
  deploy_dir="${HOME}/.local/share/swarm/testbench-deploy/${repo_name}-${source_digest}"
fi
printf 'ssh-fast-test: fetching bundled ref %s commit %s without switching source branch %s\n' "${bundle_ref}" "${local_head}" "${source_branch_before:-detached}"
git bundle verify "${remote_bundle_path}" >/dev/null
git fetch --force "${remote_bundle_path}" "${bundle_ref}:refs/swarm/testbench/candidate"
if ! git cat-file -e "${local_head}^{commit}"; then
  printf 'ssh-fast-test: bundled commit %s is unavailable after fetch\n' "${local_head}" >&2
  exit 1
fi
mkdir -p -- "$(dirname -- "${deploy_dir}")"
if [ ! -e "${deploy_dir}" ]; then
  git worktree add --detach "${deploy_dir}" "${local_head}"
elif ! git -C "${deploy_dir}" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  printf 'ssh-fast-test: deployment path %s is not a git worktree\n' "${deploy_dir}" >&2
  exit 1
fi
source_common="$(cd "$(git rev-parse --git-common-dir)" && pwd -P)"
deploy_common="$(cd "${deploy_dir}" && cd "$(git rev-parse --git-common-dir)" && pwd -P)"
if [ "${source_common}" != "${deploy_common}" ]; then
  printf 'ssh-fast-test: deployment worktree %s belongs to another repository\n' "${deploy_dir}" >&2
  exit 1
fi
git -C "${deploy_dir}" checkout --detach "${local_head}"
git -C "${deploy_dir}" reset --hard "${local_head}"
git -C "${deploy_dir}" clean -fdx -e .tools/go/ -e .cache/ -e web/node_modules/ -e node_modules/
current_head="$(git -C "${deploy_dir}" rev-parse --verify HEAD)"
if [ "${current_head}" != "${local_head}" ]; then
  printf 'ssh-fast-test: deployment HEAD %s != bundled HEAD %s\n' "${current_head}" "${local_head}" >&2
  exit 1
fi
source_head_after="$(git -C "${source_dir}" rev-parse --verify HEAD)"
source_branch_after="$(git -C "${source_dir}" branch --show-current 2>/dev/null || true)"
if [ "${source_head_after}" != "${source_head_before}" ] || [ "${source_branch_after}" != "${source_branch_before}" ]; then
  printf 'ssh-fast-test: source workspace branch or HEAD changed during deployment\n' >&2
  exit 1
fi
printf 'ssh-fast-test: deployment worktree %s now at %s; source remains %s at %s\n' "${deploy_dir}" "${current_head}" "${source_branch_after:-detached}" "${source_head_after}"
cd "${deploy_dir}"
if [ "${prepare_only}" = 'true' ]; then
  printf 'ssh-fast-test: prepare-only complete; rebuild and restart skipped\n'
  exit 0
fi
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
sudo_shim_dir="$(mktemp -d)"
trap 'rm -rf -- "${sudo_shim_dir}"' EXIT
cat >"${sudo_shim_dir}/sudo" <<'SUDO_SHIM'
#!/usr/bin/env bash
set -euo pipefail
admin_broker='/usr/local/sbin/swarm-testbench-admin'
[[ $# -ge 1 ]] || { printf 'ssh-fast-test: privileged command is required\n' >&2; exit 2; }
if [[ "$1" == '-n' ]]; then shift; fi
[[ $# -ge 1 ]] || { printf 'ssh-fast-test: privileged command is required\n' >&2; exit 2; }
case "$(basename -- "$1")" in
  systemctl) shift ;;
  *) exit 1 ;;
esac
case "$*" in
  'daemon-reload') action='swarm-service-reload' ;;
  'start swarm.service'|'restart swarm.service') action='swarm-service-restart' ;;
  'stop swarm.service') action='swarm-service-stop' ;;
  *) printf 'ssh-fast-test: rebuild requested unsupported system service action\n' >&2; exit 1 ;;
esac
exec /usr/bin/sudo -n "${admin_broker}" "${action}"
SUDO_SHIM
chmod 0700 "${sudo_shim_dir}/sudo"
SWARM_SKIP_SYSTEMD_UNIT=1 PATH="${sudo_shim_dir}:${PATH}" ./rebuild f
rm -rf -- "${sudo_shim_dir}"
trap - EXIT
REMOTE_SSH_FAST_TEST

if [[ "${RESTART_SERVICE}" == "true" ]]; then
  remote_service_action reload
  remote_service_action restart
  sleep 2
  remote_service_action status
fi
