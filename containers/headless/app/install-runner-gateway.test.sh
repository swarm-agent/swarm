#!/usr/bin/env bash
# Purpose: runner_gateway_cmd and install_units must persist a runner-only
# Serve endpoint without app capabilities, preserve it during unit regeneration,
# and fail rather than announce success when publishing fails. Stub host edges.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
# Like install-publish.test.sh, exercise only temporary files without host root.
eval "$(declare -f runner_gateway_cmd | sed '/\[\[ $EUID -eq 0 \]\]/d')"
STATE=$(mktemp -d "${TMPDIR:?}/runner-gateway.XXXXXX")
trap 'rm -rf "$STATE"' EXIT
CONF=$STATE/install.env
UNITS=$STATE/units
OPT=$STATE/opt
DATA_DIRS=("$STATE/config")
APP_CONFIG=$STATE/app-config
# Ownership is a host boundary; retain real temporary directory creation.
install() { if [[ $* == '-d -o '* ]]; then mkdir -p "${DATA_DIRS[0]}" "$APP_CONFIG"; else command install "$@"; fi; }
mkdir -p "$OPT/current"
printf '%s\n' 'TAILNET=on' 'PERMISSIONS=auto' 'AUTO_UPDATE=off' 'APP_ORIGIN=http://localhost' 'APP_RELAY_URL=' 'APP_DEVICE_NAME=test' >"$CONF"
log=$STATE/calls
systemctl() { echo "systemctl $*" >>"$log"; }
docker() { echo systemd; }
tailscale() {
 echo "tailscale $*" >>"$log"
 if [[ $* == 'status --json' ]]; then echo '{"Self":{"DNSName":"hub.example.ts.net."}}'; fi
 if [[ ${FAIL_SERVE:-0} == 1 && $* == 'serve --bg'* ]]; then return 1; fi
}
main runner-gateway on >"$STATE/output"
grep -q '^RUNNER_GATEWAY_PORT=7784$' "$CONF"
grep -q -- '--runner-gateway-port=7784' "$UNITS/$DAEMON_UNIT"
grep -q 'tailscale serve --bg --https=8445 http://127.0.0.1:7784' "$log"
! grep -q -- '--accept-app-caps' "$log"
grep -q 'https://hub.example.ts.net:8445' "$STATE/output"
grep -q 'tcp:8445' "$STATE/output"
grep -q "systemctl restart $DAEMON_UNIT" "$log"
install_units
grep -q -- '--runner-gateway-port=7784' "$UNITS/$DAEMON_UNIT"
main runner-gateway status >"$STATE/status"
grep -q 7784 "$STATE/status"
grep -q 'tailscale serve status' "$log"
main runner-gateway off
! grep -q '^RUNNER_GATEWAY_PORT=' "$CONF"
! grep -q -- '--runner-gateway-port=' "$UNITS/$DAEMON_UNIT"
grep -q 'tailscale serve --https=8445 off' "$log"
if (FAIL_SERVE=1; main runner-gateway on) >"$STATE/failure" 2>&1; then echo 'publish failure ignored' >&2; exit 1; fi
! grep -q 'Runner hub URL:' "$STATE/failure"
echo 'runner gateway installer checks passed (stubbed host commands)'
