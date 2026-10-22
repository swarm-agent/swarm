#!/usr/bin/env bash
# Purpose: exercise real activate under conditional invocation (errexit off),
# proving a rejected reservation makes no host changes and failures propagate.
# Authority: activate/install_mcp_launcher; real packaged bridge files ensure
# the added activation prerequisite cannot mask the existing failure cases.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
OPT=$work/opt CONF=$work/conf SWARMCTL=$work/swarmctl SWARM_MCP=$work/swarm-mcp
mkdir -p "$OPT/releases/new/bin" "$OPT/releases/old/bin"
touch "$OPT/releases/new/bin/swarmd"; chmod +x "$OPT/releases/new/bin/swarmd"
ln -s "$(command -v node)" "$OPT/releases/new/bin/node"
bash "$here/package-mcp.sh" "$OPT/releases/new"
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
  # activate switches before restarting; its caller owns rollback if restart
  # fails. Pre-switch failures must still leave current untouched.
  if [[ $failure == restart ]]; then
   [[ $(running_commit) == new && $UPDATE_SWITCHED == true ]] || exit 1
  else
   [[ $(running_commit) == old ]] || exit 1
  fi
  ! grep -q 'Swarm is running' "$work/output" || exit 1
  if [[ $failure == claim ]]; then
   [[ $(cat "$CONF") == original && ! -e $SWARMCTL && ! -e $OPT/previous ]] || exit 1
  fi
 fi
done
# Purpose: activate must accept older releases without the optional bridge and
# print_connect must not advertise a stale launcher. This function-level fixture
# exercises the real activation boundary without changing host services.
mv "$OPT/releases/new/share/swarm-mcp" "$work/bridge"
for launcher in absent stale; do
 SWARM_MCP=$work/$launcher-mcp
 if [[ $launcher == stale ]]; then
  printf '#!/bin/sh\nexit 1\n' >"$SWARM_MCP"
  chmod +x "$SWARM_MCP"
 fi
 failure=success
 activate new >"$work/output" 2>"$work/warning"
 [[ $(running_commit) == new ]] || exit 1
 [[ $(wc -l <"$work/warning") == 1 ]] || exit 1
 grep -q 'Warning: release has no stdio MCP bridge' "$work/warning"
 [[ $launcher != absent || ! -e $SWARM_MCP ]] || exit 1
 print_connect tester >"$work/connect"
 grep -q 'codex mcp add swarm --url' "$work/connect"
 grep -q 'claude mcp add --scope user --transport http' "$work/connect"
 ! grep -q 'mcp add swarm -- swarm-mcp' "$work/connect"
done
echo 'activation tests passed'
