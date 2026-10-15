#!/usr/bin/env bash
# Purpose: exercise install_units (also called by update) and inspect generated
# priority/security directives without touching host systemd or Docker.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
work=$(mktemp -d "${TMPDIR:?}/cpu-units.XXXXXX")
trap 'rm -rf "$work"' EXIT
source <(sed '$d' "$here/install.sh")
set -euo pipefail
load_conf() { :; }
conf_migrate() { :; }
systemctl() { :; }
gateway_port() { echo 7784; }
UNITS=$work/units OPT=$work/opt SRC=$work/src PROJECT=$work/project
# Unit generation is the boundary under test, not system account installation.
install() { if [[ $1 == -d && ${2:-} == -o ]]; then mkdir -p "${@: -2}"; else command install "$@"; fi; }
SERVICE_USER=test
DATA_DIRS=("$work/data") APP_CONFIG=$work/config
PERMISSIONS=ask APP_ORIGIN=http://127.0.0.1 APP_RELAY_URL='' APP_DEVICE_NAME=test
mkdir -p "$OPT/current" "$SRC" "$PROJECT"
install_units
for directive in Slice=swarm.slice CPUWeight=1000 IOWeight=1000 ProtectControlGroups=yes; do
 grep -qx "$directive" "$UNITS/$DAEMON_UNIT"
done
grep -q -- '--sandbox-cgroup-parent=swarm-sandbox.slice' "$UNITS/$DAEMON_UNIT"
grep -qx CPUWeight=25 "$UNITS/swarm-sandbox.slice"
grep -qx IOWeight=25 "$UNITS/swarm-sandbox.slice"
echo 'CPU unit generation passed'
