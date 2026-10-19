#!/usr/bin/env bash
# Purpose: the root installer must require continuous verified idle and a
# successful daemon signal. Stub only host boundaries; never execute shutdown.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
STATE=$(mktemp -d "${TMPDIR:?}/idle-off.XXXXXX")
trap 'rm -rf "$STATE"' EXIT
printf '2\n' >"$STATE/idle-off-minutes"
response='{"busy":false,"sessions":0,"worker_runs":0,"followups":0,"reviews":0}'
local_api() { [[ $response != unreachable ]] || return 1; printf '%s' "$response"; }
OPT=$STATE
mkdir -p "$OPT/current/bin"
ln -s "$(command -v node)" "$OPT/current/bin/node"
clock=100
cut() { echo "$clock"; }
signals=0 shutdowns=0
curl() { signals=$((signals+1)); }
systemctl() { [[ $1 == poweroff ]]; shutdowns=$((shutdowns+1)); }
idle_off_check
clock=219; idle_off_check
[[ $shutdowns == 0 ]]
clock=220; idle_off_check
[[ $shutdowns == 1 && $signals == 1 ]]
for response in true unreachable malformed '{"busy":false,"reviews":1}' '{"busy":"false","reviews":0}' '{"busy":"false","sessions":0,"worker_runs":0,"followups":0,"reviews":0}' '{"busy":false,"sessions":0,"worker_runs":0,"followups":0,"reviews":0,"environment_builds":1}'; do
 clock=300; idle_off_check
 clock=1000; idle_off_check
 [[ $shutdowns == 1 && ! -f $STATE/idle-off-since ]]
done
response='{"busy":false,"sessions":0,"worker_runs":0,"followups":0,"reviews":0}'; clock=1100; idle_off_check
clock=1219; idle_off_check
[[ $shutdowns == 1 ]]
echo 'idle-off decisions passed (no real shutdown)'
