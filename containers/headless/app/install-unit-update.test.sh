#!/usr/bin/env bash
# Purpose: install_units must expose the retained installer to swarm_status.
# Prevent a generated daemon unit that leaves AI clients unable to find updates.
# Exercise real unit rendering in private scratch; replace only host service
# operations and ownership setup, never start services or perform an update.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
work=$(mktemp -d "${TMPDIR:?}/unit-update.XXXXXX")
trap 'rm -rf "$work"' EXIT
STATE=$work/state OPT=$work/opt UNITS=$work/units CONF=$work/state/install.env
APP_CONFIG=$work/app DATA_DIRS=("$work/data")
SERVICE_USER=$(id -un)
APP_ORIGIN='' APP_RELAY_URL='' APP_DEVICE_NAME=''
mkdir -p "$STATE"
printf 'TAILNET=off\nPERMISSIONS=auto\nSANDBOX=off\nENVIRONMENTS=off\nAUTO_UPDATE=off\n' >"$CONF"
systemctl() { :; }
install() { if [[ $1 == -d && ${2:-} == -o ]]; then mkdir -p "$work/data" "$APP_CONFIG"; else command install "$@"; fi; }
install_units "$OPT/releases/test"
grep -Fx "Environment=SWARM_INSTALLER=$STATE/install.sh" "$UNITS/$DAEMON_UNIT" || { echo 'FAIL: daemon unit lacks retained installer path' >&2; exit 1; }
echo 'installer update discovery tests passed'
