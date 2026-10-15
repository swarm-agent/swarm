#!/usr/bin/env bash
# Purpose: exercise real activate under conditional invocation (errexit off),
# proving a rejected reservation makes no host changes and failures propagate.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
OPT=$work/opt CONF=$work/conf SWARMCTL=$work/swarmctl
mkdir -p "$OPT/releases/new/bin" "$OPT/releases/old/bin"
touch "$OPT/releases/new/bin/swarmd"; chmod +x "$OPT/releases/new/bin/swarmd"
ln -s "$OPT/releases/old" "$OPT/current"
echo original >"$CONF"
UPDATE_DRAIN_GENERATION=boot SWARM_REF=dev
json_get() { cat >/dev/null; echo true; }
local_api() { [[ $failure != claim ]] || return 1; echo '{"reserved":true}'; }
docker() { [[ $failure != docker ]]; }
firewall() { :; }; register_gvisor() { :; }; choose_runtime() { SANDBOX_RUNTIME=test; }
install_units() { [[ $failure != units ]]; }
systemctl() { [[ $failure != restart ]]; }
for failure in claim docker units restart success; do
 ln -sfn "$OPT/releases/old" "$OPT/current"
 echo original >"$CONF"
 if activate new >"$work/output"; then
  [[ $failure == success ]] || exit 1
  [[ $(running_commit) == new ]] || exit 1
 else
  [[ $failure != success ]] || exit 1
  [[ $(running_commit) == old ]] || exit 1
  ! grep -q 'Swarm is running' "$work/output" || exit 1
  if [[ $failure == claim ]]; then
   [[ $(cat "$CONF") == original && ! -e $SWARMCTL && ! -e $OPT/previous ]] || exit 1
  fi
 fi
done
echo 'activation tests passed'
