#!/usr/bin/env bash
# Hermetic test of the installer's gated update (install.sh update): GitHub's
# commit statuses, Swarm's API, systemd, Docker and the build are stand-ins;
# a local bare repository is the branch. No network, no root.
# Run: bash install-update.test.sh
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

# The installer's functions, without running it.
# shellcheck disable=SC1090
source <(sed '$d' "$here/install.sh")
set +e +o pipefail
set -eu

STATE=$work/state OPT=$work/opt SRC=$work/src CONF=$work/state/install.env
STARTUP_CONFIG=$work/swarm.conf
UPDATE_STATE=$OPT/update-state.json UPDATE_BAD=$STATE/update-bad
mkdir -p "$STATE" "$OPT/releases" "$work/statuses"
log=$work/log
: >"$log"

# The branch on "GitHub" and the machine's checkout of it.
git init -q --bare "$work/origin.git"
git init -q -b dev "$work/dev"
commit() { git -C "$work/dev" -c user.name=t -c user.email=t@example.invalid commit -q --allow-empty -m "$1"; git -C "$work/dev" push -q "$work/origin.git" HEAD:dev; git -C "$work/dev" rev-parse HEAD; }
c1=$(commit one)
git clone -q -b dev "$work/origin.git" "$SRC"
cat >"$CONF" <<EOF
SWARM_REF=dev
AUTO_UPDATE=on
APP_ORIGIN=http://127.0.0.1:8443
APP_DEVICE_NAME=test-box
UPDATE_HOOK=$work/hook
EOF

# A release per commit: node is the real one (json_get), swarmd a stand-in.
release() { mkdir -p "$OPT/releases/$1/bin"; ln -sf "$(command -v node)" "$OPT/releases/$1/bin/node"; printf '#!/bin/sh\n' >"$OPT/releases/$1/bin/swarmd"; chmod +x "$OPT/releases/$1/bin/swarmd"; touch "$OPT/releases/$1/.complete"; }
# 0. A first install has no release yet: asking which one runs is no error
# (under errexit it stopped the first install before Swarm started).
rc=0; first=$(running_commit) || rc=$?
[[ $rc == 0 && -z $first ]] || fail "running_commit failed ($rc) or named a release on a machine without one: $first"
release "$c1"
ln -sfn "$OPT/releases/$c1" "$OPT/current"

# Stand-ins. GitHub statuses: $work/statuses/<sha> (newest first), else none.
curl() {
  local url=${*: -1}
  if [[ $url == https://api.github.com/repos/swarm-agent/swarm/commits/*/statuses* ]]; then
    local sha=${url#*/commits/}; sha=${sha%%/*}
    cat "$work/statuses/$sha" 2>/dev/null || echo '[]'
    return 0
  fi
  [[ $url == http://127.0.0.1:8443/ ]] && return 0
  echo "unexpected curl $url" >&2; return 1
}
github_repo() { echo swarm-agent/swarm; }
status() { printf '{"context":"%s","state":"%s","creator":{"login":"%s"}}' "$1" "$2" "$3"; }
reviewed() { echo "[$(status ai-review "$2" swarm-go-ai-review[bot])]" >"$work/statuses/$1"; }
busy=false
local_api() {
  case $1 in
    /v3/box/drain*)
      echo "drain $*" >>"$log"
      if [[ ${drain_http:-200} != 200 ]]; then
        printf '\n%s' "$drain_http"
        return "${drain_error:-22}"
      fi
      printf '{"generation":"test-boot","draining":true,"ready":%s,"timed_out":%s}' "$([[ $busy == true ]] && echo false || echo true)" "$busy"
      [[ " $* " != *' -w '* ]] || printf '\n200'
      ;;
    /v3/box/busy)
      echo busy >>"$log"
      [[ ${busy_error:-0} == 0 ]] || return "$busy_error"
      printf '{"busy":%s,"sessions":%s,"worker_runs":0,"environment_builds":0}' "$busy" "$([[ $busy == true ]] && echo 1 || echo 0)"
      ;;
    /v3/box/summary) printf '{"machine":{"commit":"%s"},"sandbox":{"active":true}}' "$(running_commit)" ;;
  esac
}
systemctl() { return 0; }
sleep() { :; }
# The fetched installer: finish-update is recorded, build-release builds.
INSTALLER=$work/installer
cat >"$INSTALLER" <<EOF
#!/usr/bin/env bash
case \$1 in
  finish-update) echo finish-update >>"$log" ;;
  build-release) [[ -f "$work/build-fails" ]] && exit 1; mkdir -p "$OPT/releases/\$2/bin"; ln -sf "$(command -v node)" "$OPT/releases/\$2/bin/node"; printf '#!/bin/sh\n' >"$OPT/releases/\$2/bin/swarmd"; chmod +x "$OPT/releases/\$2/bin/swarmd"; echo "build \$2" >>"$log" ;;
esac
EOF
# Activation switches the release; a commit listed in $work/broken never comes up.
activate() { echo "activate $1" >>"$log"; [[ -n $(running_commit) && $(running_commit) != "$1" ]] && ln -sfn "$OPT/releases/$(running_commit)" "$OPT/previous"; ln -sfn "$OPT/releases/$1" "$OPT/current"; }
came_up() { ! grep -qx "$1" "$work/broken" 2>/dev/null; }
printf '#!/bin/sh\necho "$SWARM_UPDATE_EVENT $SWARM_UPDATE_COMMIT $SWARM_UPDATE_MACHINE" >>%s\n' "$work/hook-log" >"$work/hook"
chmod 0755 "$work/hook"
# The hook must be root-owned; the test runs as its owner, so pretend.
stat() { if [[ $1 == -c && $3 == "$work/hook" ]]; then echo "0 $(command stat -c %a "$3")"; else command stat "$@"; fi; }
state() { json_get "$1" <"$UPDATE_STATE"; }
run_update() { (update) || true; }

# 1. The check must be posted by the named App: a passing ai-review from anyone
# else does not count, and the newest status of the right App wins.
c2=$(commit two)
echo "[$(status ai-review success someone-else), $(status ai-review failure swarm-go-ai-review[bot]), $(status ai-review success swarm-go-ai-review[bot])]" >"$work/statuses/$c2"
[[ $(check_state swarm-agent/swarm "$c2" ai-review swarm-go-ai-review[bot]) == failure ]] || fail "a status from another creator counted"
run_update
[[ $(state state) == blocked && $(git -C "$SRC" rev-parse HEAD) == "$c1" ]] || fail "a commit whose review failed was taken: $(cat "$UPDATE_STATE")"
[[ $(cat "$work/hook-log") == "blocked $c2 test-box" ]] || fail "the hook was not told of the block: $(cat "$work/hook-log" 2>/dev/null)"
run_update
[[ $(wc -l <"$work/hook-log") == 1 ]] || fail "the hook was told twice of the same block"

# 2. A commit not reviewed yet waits; once reviewed it is taken and the
# fetched installer finishes the update.
c3=$(commit three)
echo "[$(status ai-review pending swarm-go-ai-review[bot])]" >"$work/statuses/$c3"
run_update
[[ $(state state) == waiting_review && $(git -C "$SRC" rev-parse HEAD) == "$c1" ]] || fail "an unreviewed commit was taken: $(cat "$UPDATE_STATE")"
reviewed "$c3" success
run_update
[[ $(git -C "$SRC" rev-parse HEAD) == "$c3" ]] && grep -qx finish-update "$log" || fail "a reviewed commit was not taken"

# 3. While anything runs, the new build waits next to the running one.
busy=true
finish_update && fail 'busy drain unexpectedly activated'
[[ $(state state) == blocked && $(running_commit) == "$c1" ]] || fail "Swarm was restarted while work ran: $(cat "$UPDATE_STATE")"
grep -q "^activate" "$log" && fail "activated while busy"
grep -qx "build $c3" "$log" || fail "the update did not build while work ran"

# Compatibility invariant: finish_update may use the older busy endpoint only
# after an HTTP 404, and cleanup_update_drain must not touch an unowned drain.
# These function-level cases are the narrowest layer exercising the real wait,
# state and shared switch/rollback decisions without services or a release build.
# Errors, malformed status and timeout must leave the running release unchanged.
for scenario in idle becomes-idle timeout busy-missing busy-invalid server-error transport-error rollback; do
  (
    ln -sfn "$OPT/releases/$c1" "$OPT/current"
    : >"$log"
    drain_http=404 drain_error=22 busy=false busy_error=0
    UPDATE_DRAIN_SECONDS=10
    case $scenario in
      becomes-idle | timeout) busy=true ;;
      busy-missing) busy_error=22 ;;
      busy-invalid) busy=null ;;
      server-error) drain_http=500 ;;
      transport-error) drain_http=000 drain_error=7 ;;
      rollback) echo "$c3" >"$work/broken" ;;
    esac
    sleep() {
      echo "sleep $1" >>"$log"
      SECONDS=$((SECONDS + $1))
      [[ $scenario != becomes-idle ]] || busy=false
    }
    rc=0
    finish_update >"$work/fallback-out" 2>"$work/fallback-err" || rc=$?
    [[ ! -s $work/fallback-err ]] || fail "$scenario emitted errors: $(cat "$work/fallback-err")"
    [[ $(grep -c '^drain ' "$log") == 1 ]] || fail "$scenario tried to clean up or reserve a missing drain"
    case $scenario in
      idle | becomes-idle)
        [[ $rc == 0 && $(state state) == installed && $(running_commit) == "$c3" ]] || fail "$scenario did not switch"
        grep -qx "activate $c3" "$log" || fail "$scenario bypassed shared activation"
        grep -q 'draining the older way' "$work/fallback-out" || fail "$scenario did not explain legacy draining"
        [[ $scenario != becomes-idle ]] || { [[ $(grep -c '^busy$' "$log") == 2 ]] && grep -qx 'sleep 5' "$log"; } || fail "did not wait for idle"
        ;;
      rollback)
        [[ $rc == 0 && $(state state) == rolled_back && $(running_commit) == "$c1" ]] || fail "legacy update did not roll back"
        grep -qx "activate $c3" "$log" && grep -qx "activate $c1" "$log" || fail "legacy rollback bypassed activation"
        ;;
      *)
        [[ $rc != 0 && $(state state) == blocked && $(running_commit) == "$c1" ]] || fail "$scenario did not block safely"
        ! grep -q '^activate ' "$log" || fail "$scenario activated"
        case $scenario in
          server-error | transport-error) ! grep -qx busy "$log" || fail "$scenario used fallback" ;;
          busy-missing) [[ $(state reason) == *'busy endpoint missing or unavailable; daemon left running'* ]] || fail "missing busy message" ;;
          timeout) [[ $(state reason) == *'timed out'* ]] || fail "missing timeout message" ;;
        esac
        ;;
    esac
  )
done
rm -f "$work/broken" "$UPDATE_BAD"
ln -sfn "$OPT/releases/$c1" "$OPT/current"

# 4. Once nothing runs, it switches and reports it.
busy=false
finish_update
[[ $(state state) == installed && $(running_commit) == "$c3" ]] || fail "the idle machine did not switch: $(cat "$UPDATE_STATE")"
grep -qx "installed $c3 test-box" "$work/hook-log" || fail "the hook was not told of the install"
finish_update
[[ $(state state) == current ]] || fail "an up-to-date machine is not current"

# 5. A build that does not come up goes back to the one before, and that
# commit is never tried again.
c4=$(commit four)
reviewed "$c4" success
echo "$c4" >"$work/broken"
run_update
finish_update
[[ $(state state) == rolled_back && $(running_commit) == "$c3" && $(cat "$UPDATE_BAD") == "$c4" ]] || fail "a broken build was not rolled back: $(cat "$UPDATE_STATE")"
grep -qx "rolled_back $c4 test-box" "$work/hook-log" || fail "the hook was not told of the rollback"
activations=$(grep -c "^activate" "$log")
finish_update
[[ $(grep -c "^activate" "$log") == "$activations" ]] || fail "a rolled-back commit was tried again"

# 6. A failed build keeps the running build.
c5=$(commit five)
reviewed "$c5" success
touch "$work/build-fails"
run_update
(finish_update) || true
[[ $(state state) == failed && $(running_commit) == "$c3" ]] || fail "a failed build changed the running build: $(cat "$UPDATE_STATE")"
rm "$work/build-fails"

# 7. A release merge that adds nothing to a reviewed dev commit counts as it;
# one that changes the tree needs its own review.
git -C "$work/dev" checkout -q -b main "$c1"
git -C "$work/dev" -c user.name=t -c user.email=t@example.invalid merge -q --no-ff -m release "$c3"
merge=$(git -C "$work/dev" rev-parse HEAD)
git -C "$work/dev" push -q "$work/origin.git" main
git -C "$SRC" fetch -q origin main
checks_passed "$merge" >/dev/null || fail "a release merge of a reviewed commit did not pass"
echo extra >"$work/dev/extra" && git -C "$work/dev" add extra && git -C "$work/dev" -c user.name=t -c user.email=t@example.invalid commit -q --amend -m "release with a change"
changed=$(git -C "$work/dev" rev-parse HEAD)
git -C "$work/dev" push -q -f "$work/origin.git" main
git -C "$SRC" fetch -q origin main
set +e; checks_passed "$changed" >/dev/null; rc=$?; set -e
[[ $rc == 2 ]] || fail "a release merge that changed the tree passed without its own review ($rc)"
UPDATE_CHECKS=none checks_passed "$changed" || fail "UPDATE_CHECKS=none still checked"

# 8. A hook others can change is not run.
chmod 0777 "$work/hook"
: >"$work/hook-log"
SWARM_REF=dev UPDATE_HOOK=$work/hook update_state blocked "test" "other-commit" 2>/dev/null
[[ ! -s $work/hook-log ]] || fail "a world-writable hook was run"

# 9. A setting change (workspace add, daemon-env, tailnet, secret gateway)
# restarts Swarm only when nothing runs; still busy at the limit, it does not
# restart and says the change waits for the next restart.
systemctl() { [[ $1 == restart ]] && echo "restart $2" >>"$log"; return 0; }
: >"$log"
busy=true
set +e; out=$(RESTART_WAIT_SECONDS=30 restart_daemon_when_idle 2>&1); rc=$?; set -e
[[ $rc != 0 ]] && ! grep -q '^restart' "$log" || fail "Swarm was restarted while work ran ($rc): $out"
[[ $out == *"not restarted"*"run the same command again"* ]] || fail "a busy machine did not say what to do: $out"
busy=false
(restart_daemon_when_idle) || fail "an idle machine was not restarted"
grep -qx "restart $DAEMON_UNIT" "$log" || fail "the idle restart did not happen: $(cat "$log")"
systemctl() { return 0; }

# 10. The origin's GitHub name, in every form git writes it.
unset -f github_repo
source <(sed '$d' "$here/install.sh" | sed -n '/^github_repo() {/,/^}/p')
for url in https://github.com/swarm-agent/swarm.git git@github.com:swarm-agent/swarm.git ssh://git@github.com/swarm-agent/swarm; do
  git -C "$SRC" remote set-url origin "$url"
  [[ $(github_repo) == swarm-agent/swarm ]] || fail "origin $url read as $(github_repo)"
done
git -C "$SRC" remote set-url origin https://example.com/swarm.git
github_repo >/dev/null && fail "a non-GitHub origin was read as GitHub"

# Purpose: exercise the real activation and smoke/rollback boundary when the
# candidate daemon exits 2, including a failed reservation release. Service and
# Docker adapters are local stand-ins; no live outage or benchmark is claimed.
(
  source <(sed '$d' "$here/install.sh" | sed -n '/^activate() {/,/^}/p; /^came_up() {/,/^}/p')
  STARTUP_CONFIG=$work/swarm.conf
  printf '# keep comment\n sandbox_cpu_slots = 0\nport = 7777\n' >"$STARTUP_CONFIG"
  cp "$STARTUP_CONFIG" "$work/config-before"
  chmod 0600 "$STARTUP_CONFIG"
  release "$c5"
  cat >"$OPT/releases/$c5/bin/swarmd" <<'DAEMON'
#!/bin/sh
if [ "$*" = 'config removed-keys' ]; then echo sandbox_cpu_slots; exit 0; fi
exit 2
DAEMON
  INSTALLER=$work/already-built
  printf '#!/bin/sh\nexit 0\n' >"$INSTALLER"
  ln -sfn "$OPT/releases/$c3" "$OPT/current"
  SWARMCTL=$work/swarmctl
  SERVICE_USER=test SERVICE_HOME=$work
  firewall() { :; }; register_gvisor() { :; }; choose_runtime() { SANDBOX_RUNTIME=default; }; install_units() { :; }; docker() { :; }
  systemctl() {
    if [[ $1 == restart && $2 == "$DAEMON_UNIT" ]]; then
      ! grep -q 'sandbox_cpu_slots' "$STARTUP_CONFIG" || fail 'removed key present at restart'
      "$OPT/current/bin/swarmd" || return $?
    elif [[ $1 == is-active ]]; then
      [[ $(running_commit) != "$c5" ]]
    fi
  }
  local_api() {
    if [[ $(running_commit) == "$c5" ]]; then return 7; fi
    case $1 in
      /v3/box/drain*)
        if [[ " $* " == *' -X POST '* ]]; then printf '{"generation":"boot","ready":true}\n200'
        elif [[ " $* " == *'release'* ]]; then return 7
        else echo '{"reserved":true}'; fi ;;
      /v3/box/summary) printf '{"machine":{"commit":"%s"},"sandbox":{"active":true}}' "$(running_commit)" ;;
    esac
  }
  finish_update || fail 'crash-loop update did not complete rollback'
  [[ $(state state) == rolled_back && $(running_commit) == "$c3" ]] || fail 'crash-loop did not restore previous release'
  ! grep -q 'sandbox_cpu_slots' "$STARTUP_CONFIG" || fail 'removed key remains'
  backups=("$STARTUP_CONFIG".removed-keys-*)
  [[ ${#backups[@]} == 1 ]] && cmp -s "$work/config-before" "${backups[0]}" || fail 'missing exact dated backup'
  [[ $(command stat -c %a "$STARTUP_CONFIG") == 600 ]] || fail 'config mode changed'
  [[ $(cat "$STARTUP_CONFIG") == $'# keep comment\nport = 7777' ]] || fail 'unrelated config changed'
  migrate_removed_startup_keys "$OPT/releases/$c5" || fail 'repeat migration failed'
  backups=("$STARTUP_CONFIG".removed-keys-*)
  [[ ${#backups[@]} == 1 ]] || fail 'repeat migration made another backup'
  cp "$STARTUP_CONFIG" "$work/config-migrated"
  # Failed candidate introspection must not modify the managed configuration.
  migrate_removed_startup_keys "$work/missing-release" && fail 'missing candidate accepted'
  cmp -s "$STARTUP_CONFIG" "$work/config-migrated" || fail 'failed query changed config'
  # An asynchronously failing service can report restart success; socket smoke
  # must still reject it within its bounded attempts.
  ln -sfn "$OPT/releases/$c5" "$OPT/current"
  systemctl() { return 0; }
  came_up "$c5" true && fail 'socket-dead daemon passed smoke'
  systemctl() { return 1; }
  local_api() { echo '{"machine":{},"sandbox":{"active":true}}'; }
  came_up "$c5" true && fail 'inactive service passed smoke'
  true
)

echo "install-update: all checks passed"
