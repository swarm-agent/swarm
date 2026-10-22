#!/usr/bin/env bash
# Purpose: main and service_user_run must leave the caller's directory before
# crossing user boundaries. A runuser boundary spy is the narrowest portable
# test (no root/account mutation); Git's explicit -C / is also exercised from
# a removed cwd, reproducing the inaccessible-cwd failure without privileges.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
work=$(mktemp -d)
trap 'cd /; rm -rf "$work"' EXIT
install_all() { [[ $PWD == / ]]; }
(cd "$work"; main install)
runuser() { [[ $PWD == / ]]; }
(cd "$work"; service_user_run true)
mkdir "$work/home" "$work/gone"
(
  cd "$work/gone"
  rmdir "$work/gone"
  HOME=$work/home git -C / config --global user.name 'Fixture agent'
  [[ $(HOME=$work/home git -C / config --global user.name) == 'Fixture agent' ]]
)
# Purpose: print_connect advertises persistent registration only when the real
# packaged bridge and launcher exist; activation tests cover absent payloads.
SWARM_MCP=$work/swarm-mcp OPT=$work/opt
mkdir -p "$OPT/current/bin"
ln -s "$(command -v node)" "$OPT/current/bin/node"
bash "$here/package-mcp.sh" "$OPT/current"
install_mcp_launcher "$OPT/current"
print_connect fixture >"$work/message"
grep -q 'codex mcp add swarm -- swarm-mcp' "$work/message"
! grep -q 'bearer-token-env-var' "$work/message"
echo 'PASS: cwd boundaries and persistent registration instructions'
