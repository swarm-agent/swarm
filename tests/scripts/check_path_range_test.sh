#!/usr/bin/env bash
# Requirement: a protected push is judged on the lines its commits add. Reviewed
# paths already on the target branch must not refuse the push, a path the push
# adds must, and without a base the checks keep scanning the whole repository.
# Threat: whole-repo scans refuse every push once reviewed content lands by
# another route, so people bypass the hook; a scoped scan that drops lines lets
# a new hardcoded path through.
# Authority: scripts/lib-check-range.sh, scripts/check-hardcoded-paths.sh,
# scripts/check-daemon-storage-paths.sh and base selection in
# scripts/check-prepush.sh.
# Layer: the real check scripts run in a throwaway Git repository; precommit
# and critical tests are stubbed only for the pre-push base-selection cases.
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
TEST_ROOT="$(mktemp -d "${TMPDIR:-/tmp}/swarm-path-range-test.XXXXXX")"
trap 'rm -rf "${TEST_ROOT}"' EXIT

export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME=test GIT_AUTHOR_EMAIL=test@example.invalid
export GIT_COMMITTER_NAME=test GIT_COMMITTER_EMAIL=test@example.invalid
unset SWARM_CHECK_BASE SWARM_CHECK_HEAD SWARM_PREPUSH_CHECKED

fail() {
  echo "check_path_range_test: FAIL: $*" >&2
  [[ -f "${TEST_ROOT}/out" ]] && sed 's/^/  | /' "${TEST_ROOT}/out" >&2
  exit 1
}

repo="${TEST_ROOT}/repo"
mkdir -p "${repo}/scripts" "${repo}/app" "${repo}/swarmd/internal/run"
cp "${ROOT_DIR}/scripts/check-hardcoded-paths.sh" "${ROOT_DIR}/scripts/check-daemon-storage-paths.sh" \
  "${ROOT_DIR}/scripts/lib-check-range.sh" "${ROOT_DIR}/scripts/check-prepush.sh" "${repo}/scripts/"
cd "${repo}"
git init -q -b dev .

commit() { git add -A && git commit -q -m "$1" && git rev-parse HEAD; }
path_check() { bash scripts/check-hardcoded-paths.sh "$@" >"${TEST_ROOT}/out" 2>&1; }
storage_check() { bash scripts/check-daemon-storage-paths.sh "$@" >"${TEST_ROOT}/out" 2>&1; }
out_has() { grep -qF -- "$1" "${TEST_ROOT}/out"; }

# Base: reviewed content that the whole-repo scans reject.
printf '%s\n' '#!/usr/bin/env bash' 'CONF=/etc/legacy.conf' 'echo ok' >app/run.sh
printf '%s\n' 'package run' '' 'func move() { _ = os.Rename("a", "b") }' >swarmd/internal/run/move.go
base="$(commit base)"

# 1. Whole-repo behavior without a base is unchanged, including the self-test.
path_check --self-test && fail "whole-repo path check passed on a base with a bad path"
out_has '[path-check] self-test PASS' || fail "path self-test did not run"
out_has './app/run.sh:2:CONF=/etc/legacy.conf' || fail "whole-repo path check lost the existing hit"
storage_check && fail "whole-repo storage check passed on a base with a migration rename"
out_has 'swarmd/internal/run/move.go:3:' || fail "whole-repo storage check lost the existing hit"

# 2. A push that adds only clean lines passes although the base has bad paths.
printf '%s\n' 'echo more' >>app/run.sh
printf '%s\n' '#!/usr/bin/env bash' 'echo clean' 'echo mid' 'echo end' >app/new.sh
printf '%s\n' '' 'func other() {}' >>swarmd/internal/run/move.go
clean="$(commit clean)"
SWARM_CHECK_BASE="${base}" SWARM_CHECK_HEAD="${clean}" path_check || fail "clean push refused by path check"
out_has 'judging only lines added' || fail "path check did not report range mode"
SWARM_CHECK_BASE="${base}" SWARM_CHECK_HEAD="${clean}" storage_check --self-test || fail "clean push refused by storage check"

# 3. A newly added bad line fails, with its real line number, and only it.
sed -i 's|^echo ok$|DATA=/opt/newthing|' app/run.sh
# Two hunks in one file (two lines, then one): each keeps its own line number.
sed -i -e '1s|.*|# changed|' -e '2s|.*|echo changed|' -e 's|^echo end$|LOG=/var/tmp/x.log|' app/new.sh
printf '%s\n' 'func copyAll() { copyDir("a", "b") }' >>swarmd/internal/run/move.go
bad="$(commit bad)"
SWARM_CHECK_BASE="${base}" SWARM_CHECK_HEAD="${bad}" path_check && fail "added bad path passed"
out_has './app/run.sh:3:DATA=/opt/newthing' || fail "added bad path not reported as ./path:line:text"
out_has '/etc/legacy.conf' && fail "range mode reported a line already in the base"
SWARM_CHECK_BASE="${base}" SWARM_CHECK_HEAD="${bad}" storage_check && fail "added migration copy passed"
out_has 'swarmd/internal/run/move.go:6:func copyAll()' || fail "added storage hit not reported"
out_has 'os.Rename("a", "b")' && fail "storage range mode reported a line already in the base"
SWARM_CHECK_BASE="${clean}" SWARM_CHECK_HEAD="${bad}" path_check && fail "added bad path in a second hunk passed"
out_has './app/new.sh:4:LOG=/var/tmp/x.log' || fail "second hunk not reported at its own line"
# SWARM_CHECK_HEAD defaults to HEAD.
SWARM_CHECK_BASE="${base}" path_check && fail "range mode without SWARM_CHECK_HEAD missed the added path"

# 4. A renamed file is judged under its new name, so moved text is re-checked.
git reset -q --hard "${clean}"
git mv app/run.sh app/moved.sh
moved="$(commit moved)"
SWARM_CHECK_BASE="${clean}" SWARM_CHECK_HEAD="${moved}" path_check && fail "renamed file escaped the path check"
out_has './app/moved.sh:2:CONF=/etc/legacy.conf' || fail "renamed file not judged under its new name"

# 5. A base that is not a commit fails closed.
SWARM_CHECK_BASE=0123456789abcdef0123456789abcdef01234567 path_check && fail "unknown base passed"
out_has 'SWARM_CHECK_BASE is not an available commit' || fail "unknown base not reported"

# 6. Pre-push picks the base: the remote commit, else the merge-base with the
# remote-tracking ref, else whole-repo checks.
git reset -q --hard "${bad}"
cat >scripts/check-precommit.sh <<'EOF'
#!/usr/bin/env bash
echo "stub-precommit base=${SWARM_CHECK_BASE:-unset} head=${SWARM_CHECK_HEAD:-unset}"
EOF
chmod +x scripts/check-precommit.sh
printf '%s\n' 'echo stub-critical' >scripts/run-critical-tests.sh
prepush() { bash scripts/check-prepush.sh origin test-remote >"${TEST_ROOT}/out" 2>&1; }
zero=0000000000000000000000000000000000000000

printf 'refs/heads/dev %s refs/heads/dev %s\n' "${bad}" "${clean}" | prepush || fail "pre-push with a known remote commit"
out_has "stub-precommit base=${clean} head=${bad}" || fail "pre-push did not pass the remote commit as base"

git update-ref refs/remotes/origin/dev "${base}"
printf 'refs/heads/dev %s refs/heads/dev %s\n' "${bad}" "${zero/0/f}" | prepush || fail "pre-push with an unknown remote commit"
out_has "stub-precommit base=${base} head=${bad}" || fail "pre-push did not fall back to the merge-base"
git update-ref -d refs/remotes/origin/dev

printf 'refs/heads/main %s refs/heads/main %s\n' "${bad}" "${zero}" | prepush || fail "pre-push to a new protected branch"
out_has 'stub-precommit base=unset head=unset' || fail "pre-push without a base did not keep whole-repo checks"
out_has 'path checks scan the whole repository' || fail "pre-push did not report the whole-repo fallback"

SWARM_CHECK_BASE="${bad}" prepush </dev/null || fail "pre-push with no refs"
out_has 'stub-precommit base=unset head=unset' || fail "pre-push with no refs kept an inherited base"

echo "check_path_range_test: PASS"
