#!/usr/bin/env bash
# Purpose: runner-only install orchestration must not configure a hub or restart
# active work. Exercise installer functions with fake apt/docker/systemctl and
# source builds; no packages, accounts, network or services are changed.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:?}/runner-install.XXXXXX")
trap 'rm -rf "$work"' EXIT
source <(sed '$d' "$here/install.sh")
source "$here/runner-install.sh"
RUNNER_STATE=$work/state RUNNER_OPT=$work/opt RUNNER_CONF=$work/etc
CONF=$work/hub.env SWARMCTL=$work/swarmctl
log=$work/log
fail() { echo "FAIL: $*" >&2; exit 1; }
# Replace only the privilege guard; all filesystem writes stay in the fixture.
runner_require_root() { :; }
uname() { echo x86_64; }
snap() { return 1; }
apt-get() { echo "apt $*" >>"$log"; }
docker() { echo "docker $*" >>"$log"; }
systemctl() { echo "systemctl $*" >>"$log"; [[ $1 != is-active ]]; }
id() { return 1; }
useradd() { echo "useradd $*" >>"$log"; }
usermod() { echo "usermod $*" >>"$log"; }
install_gvisor() { echo gvisor >>"$log"; }
register_gvisor() { echo register >>"$log"; }
runner_build() { echo "build $*" >>"$log"; RUNNER_RELEASE=$RUNNER_OPT/releases/test; mkdir -p "$RUNNER_RELEASE/bin"; }
install_runner --ref dev >"$work/output"
[[ $(cat "$RUNNER_CONF/ref") == dev ]] || fail ref
[[ $(stat -c %a "$RUNNER_CONF/ref") == 600 ]] || fail mode
[[ $(readlink "$RUNNER_OPT/current") == "$RUNNER_RELEASE" ]] || fail release
! grep -q 'docker.io' "$log" || fail 'reinstalled working Docker'
grep -q 'usermod -aG docker swarm-runner' "$log" || fail user
grep -q 'swarmctl runner join' "$work/output" || fail instructions
! grep -Eq 'headless.service|tailscale|setup' "$log" || fail 'hub setup leaked'
touch "$RUNNER_CONF/credential"
update_runner
grep -q 'systemctl start swarm-runner.service' "$log" || fail start
systemctl() { [[ $1 == is-active ]]; }
sleep() { :; } # bounded wait control, not a workload or benchmark
if (update_runner) >"$work/error" 2>&1; then fail 'active runner updated'; fi
grep -q 'not activated' "$work/error" || fail 'missing timeout error'
echo 'runner installer tests passed'
