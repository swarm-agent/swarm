#!/usr/bin/env bash
# Purpose: runner-only install orchestration must not configure a hub or restart
# active work. Exercise installer functions with fake apt/docker/systemctl and
# source builds; no packages, accounts, network or services are changed.
# runner_build/runner_activate must provision the landing image and firewall,
# preserve the active tag until a successful stop, and activate before join.
# This shell boundary is the narrowest test of Docker/systemd ordering.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:?}/runner-install.XXXXXX")
trap 'rm -rf "$work"' EXIT
source <(sed '$d' "$here/install.sh")
source "$here/runner-install.sh"
RUNNER_STATE=$work/state RUNNER_OPT=$work/opt RUNNER_CONF=$work/etc
CONF=$work/hub.env SWARMCTL=$work/swarmctl UNITS=$work/units
RUNNER_RUNTIME_STATE=$work/runtime-state
log=$work/log
fail() { echo "FAIL: $*" >&2; exit 1; }
# Replace only the privilege guard; all filesystem writes stay in the fixture.
runner_require_root() { :; }
# Account creation is recorded, so ownership flags are asserted rather than
# applied to nonexistent host accounts; modes use the real install command.
install() {
  echo "install $*" >>"$log"
  local args=()
  while (($#)); do
    case $1 in -o|-g) shift 2 ;; *) args+=("$1"); shift ;; esac
  done
  command install "${args[@]}"
}
uname() { echo x86_64; }
snap() { return 1; }
apt-get() { echo "apt $*" >>"$log"; }
fixture_commit=first
image_exists=no
service_exists=no
stop_fails=no
git() {
  if [[ $* == *rev-parse* ]]; then echo "$fixture_commit"; fi
  mkdir -p "$RUNNER_STATE/src/.git" "$RUNNER_STATE/src/containers/sandbox"
  cp "$here/../../sandbox/firewall.sh" "$RUNNER_STATE/src/containers/sandbox/firewall.sh"
}
docker() {
  echo "docker $*" >>"$log"
  if [[ $1 == image ]]; then [[ $image_exists == yes ]]; return; fi
  if [[ $1 == network && $2 == inspect ]]; then return 1; fi
  if [[ $* == *runner-export* ]]; then mkdir -p "$RUNNER_OPT/releases/$fixture_commit/bin"; fi
}
systemctl() {
  echo "systemctl $*" >>"$log"
  case $1 in
    show) if [[ $service_exists == yes ]]; then echo loaded; else echo not-found; fi ;;
    restart)
      if [[ $2 == swarm-sandbox-firewall.service ]]; then
        bash "$RUNNER_STATE/src/containers/sandbox/firewall.sh"
      fi ;;
    is-active) return 1 ;;
    stop) [[ $service_exists == yes && $stop_fails == no ]] ;;
  esac
}
iptables() { echo "iptables $*" >>"$log"; }
ip6tables() { echo "ip6tables $*" >>"$log"; }
export log image_exists RUNNER_STATE
export -f docker iptables ip6tables
id() { return 1; }
useradd() { echo "useradd $*" >>"$log"; }
usermod() { echo "usermod $*" >>"$log"; }
install_gvisor() { echo gvisor >>"$log"; }
register_gvisor() { echo register >>"$log"; }
install_runner --ref dev >"$work/output"
[[ $(cat "$RUNNER_CONF/ref") == dev ]] || fail ref
[[ $(stat -c %a "$RUNNER_CONF/ref") == 600 ]] || fail mode
[[ $(stat -c %a "$RUNNER_CONF") == 750 ]] || fail directory-mode
grep -q "install -d -o root -g swarm-runner -m 0750 $RUNNER_CONF" "$log" || fail directory-owner
[[ $(readlink "$RUNNER_OPT/current") == "$RUNNER_RELEASE" ]] || fail release
! grep -q 'docker.io' "$log" || fail 'reinstalled working Docker'
grep -q 'usermod -aG docker swarm-runner' "$log" || fail user
grep -q 'swarmctl runner join' "$work/output" || fail instructions
! grep -Eq 'headless.service|tailscale|setup' "$log" || fail 'hub setup leaked'
grep -q "docker build --platform linux/amd64 -t swarm-sandbox:first $RUNNER_STATE/src/containers/sandbox" "$log" || fail 'install image build'
grep -q 'docker tag swarm-sandbox:first swarm-sandbox:local' "$log" || fail 'install image activation'
grep -q 'SWARM_SANDBOX_NETWORK=swarm-sandbox SWARM_SANDBOX_BRIDGE=br-swarm-sbx SWARM_SANDBOX_SUBNET=172.31.251.0/24 SWARM_SANDBOX_GATEWAY_PORT=' "$UNITS/swarm-sandbox-firewall.service" || fail 'firewall environment'
grep -q 'docker network create --driver bridge --subnet 172.31.251.0/24 -o com.docker.network.bridge.name=br-swarm-sbx -o com.docker.network.bridge.enable_icc=false swarm-sandbox' "$log" || fail 'sandbox network'
grep -q 'iptables -A SWARM-SANDBOX-HOST -j DROP' "$log" || fail 'host isolation'
grep -q 'Requires=swarm-sandbox-firewall.service' "$UNITS/swarm-runner.service.d/sandbox.conf" || fail 'runner firewall dependency'
: >"$log"
image_exists=yes
install_runner --ref dev >"$work/output"
! grep -q 'docker build --platform linux/amd64 -t swarm-sandbox:' "$log" || fail 'install rebuilt existing image'
image_exists=no
: >"$log"
fixture_commit=second
update_runner
[[ $(readlink "$RUNNER_OPT/current") == "$RUNNER_OPT/releases/second" ]] || fail 'update before join activation'
! grep -Eq 'systemctl (stop|start) swarm-runner.service' "$log" || fail 'absent service touched'
grep -q 'docker build --platform linux/amd64 -t swarm-sandbox:second' "$log" || fail 'update image build'
grep -q 'docker tag swarm-sandbox:second swarm-sandbox:local' "$log" || fail 'update image activation'
: >"$log"
image_exists=yes
service_exists=yes
touch "$RUNNER_CONF/credential"
update_runner
! grep -q 'docker build --platform linux/amd64 -t swarm-sandbox:' "$log" || fail 'rebuilt existing image'
stop_fails=yes
fixture_commit=third
: >"$log"
if (update_runner) >"$work/error" 2>&1; then fail 'failed stop activated'; fi
[[ $(readlink "$RUNNER_OPT/current") == "$RUNNER_OPT/releases/second" ]] || fail 'failed stop replaced release'
! grep -q 'docker tag' "$log" || fail 'failed stop replaced image'
stop_fails=no
update_runner
grep -q 'systemctl start swarm-runner.service' "$log" || fail start
systemctl() { if [[ $1 == show ]]; then echo loaded; else [[ $1 == is-active ]]; fi; }
sleep() { :; } # bounded wait control, not a workload or benchmark
if (update_runner) >"$work/error" 2>&1; then fail 'active runner updated'; fi
grep -q 'not activated' "$work/error" || fail 'missing timeout error'
# A busy runner becomes idle before stop/activation/start; stop also closes the
# race where a new landing was claimed after the idle observation.
echo 'busy landing 2026-01-01T00:00:00Z' >"$RUNNER_RUNTIME_STATE"
systemctl() { echo "systemctl $*" >>"$log"; if [[ $1 == show ]]; then echo loaded; fi; return 0; }
sleep() { echo idle >"$RUNNER_RUNTIME_STATE"; echo waited >>"$log"; }
: >"$log"
update_runner
[[ $(grep -E 'waited|systemctl (is-active|stop|start)|docker tag' "$log") == $'systemctl is-active --quiet swarm-runner.service\nwaited\nsystemctl is-active --quiet swarm-runner.service\nsystemctl stop swarm-runner.service\ndocker tag swarm-sandbox:third swarm-sandbox:local\nsystemctl start swarm-runner.service' ]] || fail 'unexpected wait/stop/activate/start order'
grep -q waited "$log" || fail 'did not wait for idle'
grep -q 'systemctl stop swarm-runner.service' "$log" || fail 'did not stop before activation'
grep -q 'systemctl start swarm-runner.service' "$log" || fail 'did not restart'
echo 'runner installer tests passed'
