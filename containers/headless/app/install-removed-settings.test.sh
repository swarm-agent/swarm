#!/usr/bin/env bash
# Purpose: machines installed before the runner listener and landing were
# removed have RUNNER_GATEWAY_PORT in install.env and service units passing
# --runner-gateway-port. An update must drop the setting and write the daemon
# unit without the flag (the daemon still accepts and ignores it). The real
# install_units/conf_migrate run against a temporary state folder; only host
# boundaries (Docker, systemd) are stubbed.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
source <(sed '$d' "$here/install.sh")
work=$(mktemp -d "${TMPDIR:?}/removed-settings.XXXXXX")
trap 'rm -rf "$work"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
STATE=$work/state OPT=$work/opt UNITS=$work/units CONF=$work/state/install.env
mkdir -p "$STATE" "$OPT/releases/abc/bin" "$UNITS"
ln -s "$OPT/releases/abc" "$OPT/current"
printf 'TAILNET=off\nPERMISSIONS=auto\nAUTO_UPDATE=off\nRUNNER_GATEWAY_PORT=7784\n' >"$CONF"
docker() { [[ $1 == info ]] && echo systemd; }
systemctl() { :; }
install_units >"$work/out" 2>&1 || { cat "$work/out" >&2; fail "install_units failed"; }
! grep -q RUNNER_GATEWAY_PORT "$CONF" || fail "RUNNER_GATEWAY_PORT kept in install.env"
[[ -z ${RUNNER_GATEWAY_PORT+set} ]] || fail "RUNNER_GATEWAY_PORT still set"
grep -q '^ExecStart=.*/bin/swarmd ' "$UNITS/$DAEMON_UNIT" || fail "no daemon ExecStart written"
! grep -q -- '--runner-gateway-port' "$UNITS/$DAEMON_UNIT" || fail "daemon unit still passes --runner-gateway-port"
! grep -q -- '--landing-max-concurrent' "$UNITS/$DAEMON_UNIT" || fail "daemon unit still passes --landing-max-concurrent"
grep -q 'dropped the removed RUNNER_GATEWAY_PORT setting' "$work/out" || fail "migration not reported"
echo 'removed installer settings tests passed'
