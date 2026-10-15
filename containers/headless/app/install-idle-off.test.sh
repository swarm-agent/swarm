#!/usr/bin/env bash
# Purpose: the root installer must require continuous verified idle and a
# successful daemon signal. Stub only host boundaries; never execute shutdown.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
STATE=$(mktemp -d "${TMPDIR:?}/idle-off.XXXXXX")
trap 'rm -rf "$STATE"' EXIT
printf '2\n' >"$STATE/idle-off-minutes"
response=false
local_api() { [[ $response != unreachable ]] || return 1; printf '%s' "$response"; }
json_get() { cat; }
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
for response in true unreachable malformed; do
 clock=300; idle_off_check
 clock=1000; idle_off_check
 [[ $shutdowns == 1 && ! -f $STATE/idle-off-since ]]
done
response=false; clock=1100; idle_off_check
clock=1219; idle_off_check
[[ $shutdowns == 1 ]]
echo 'idle-off decisions passed (no real shutdown)'
