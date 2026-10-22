#!/usr/bin/env bash
# Purpose: build_release must package the optional MCP bridge outside Docker's
# filtered context without blocking source installs on missing/failed packaging;
# uninstall_all must remove its launcher. Exercise these shell boundaries with a
# fake exporter and intercepted host operations, the narrowest hermetic layer.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
work=$(mktemp -d)
trap 'command rm -rf "$work"' EXIT
OPT=$work/opt
STAGE=$work/stage
SRC=$(cd "$here/../../.." && pwd)
SWARM_REF=test
release_install() { return 1; }
environments_on() { return 1; }
step() { :; }
docker() {
  if [[ ${1:-} == build ]]; then
    mkdir -p "$STAGE/bin"
    printf '#!/bin/sh\n' >"$STAGE/bin/swarmd"
    chmod +x "$STAGE/bin/swarmd"
  fi
}
build_release present
[[ -f $OPT/releases/present/.complete ]]
for file in swarm-mcp swarm-mcp.mjs lib/connection.mjs; do
  cmp "$SRC/plugins/swarm/bin/$file" "$OPT/releases/present/share/swarm-mcp/$file"
done
SRC=$work/source
mkdir -p "$SRC/containers/headless/app"
for mode in missing failed; do
  if [[ $mode == failed ]]; then
    printf 'exit 1\n' >"$SRC/containers/headless/app/package-mcp.sh"
  fi
  build_release "$mode" 2>"$work/$mode.err"
  [[ -x $OPT/releases/$mode/bin/swarmd && -f $OPT/releases/$mode/.complete ]]
  [[ $(wc -l <"$work/$mode.err") == 1 ]]
  grep -q 'warning: MCP bridge packaging unavailable' "$work/$mode.err"
done

# Never run host cleanup: intercept rm, allowing only fixture launcher deletion.
SWARMCTL=$work/swarmctl
SWARM_MCP=$work/swarm-mcp
CONF=$work/no-conf
WORKSPACES_DROPIN=$work/dropin/config
WORKSPACES=$work/workspaces
UNITS=$work/units
PROJECT=$work/no-project
systemctl() { :; }
iptables() { :; }
workspace_listed() { :; }
remove_legacy_layout() { :; }
rm() {
  local arg
  for arg in "$@"; do
    case $arg in
      "$SWARMCTL"|"$SWARM_MCP") command rm -f -- "$arg" ;;
    esac
  done
}
touch "$SWARMCTL" "$SWARM_MCP" "$work/unrelated"
# Bypass only the root admission guard for this intercepted cleanup fixture.
# No privileged operations are executed by this test.
eval "$(declare -f uninstall_all | sed 's/\[\[ $EUID -eq 0 \]\]/true/')"
uninstall_all --yes >"$work/uninstall.out"
[[ ! -e $SWARMCTL && ! -e $SWARM_MCP && -f $work/unrelated ]]
echo 'source bridge packaging and uninstall tests passed'
