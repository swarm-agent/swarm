#!/bin/bash
set -euo pipefail
umask 077
config=/etc/swarmd/headless-app
mkdir -p "$config"
if [[ ! -e "$config/login-secret" ]]; then
  openssl rand -hex 32 > "$config/login-secret"
fi
# Loopback HTTP needs no certificate or trust-store setup. Explicit HTTPS uses
# operator-provided certificates; never silently generate an untrusted one.
if [[ "${APP_ORIGIN:-http://127.0.0.1:8443}" == https:* ]]; then
  [[ -s "$config/tls.key" && -s "$config/tls.crt" ]] || {
    echo 'Explicit HTTPS requires a trusted certificate and key in the config volume.' >&2; exit 1;
  }
fi
[[ -s "$config/login-secret" ]] || {
  echo 'Installation files incomplete; restore the config volume.' >&2; exit 1;
}
# Separate process groups let shutdown stop each child and its descendants.
# Logs remain private in the named volume, never provider payloads on stdout.
setsid /usr/local/bin/swarmd --desktop-port=0 --cwd=/project >>/var/log/swarmd/headless-app-daemon.log 2>&1 &
daemon=$!
setsid node /opt/workshop/examples/headless-app/server.mjs &
app=$!
shutdown() {
  trap '' TERM INT
  kill -TERM -- "-$daemon" "-$app" 2>/dev/null || true
  # Bound shutdown if either process is stuck. tini also reaps orphan descendants.
  (sleep 10; kill -KILL -- "-$daemon" "-$app" 2>/dev/null || true) &
  guard=$!
  wait "$daemon" 2>/dev/null || true
  wait "$app" 2>/dev/null || true
  kill "$guard" 2>/dev/null || true
  kill -KILL -- "-$daemon" "-$app" 2>/dev/null || true
}
trap 'shutdown; exit 143' TERM
trap 'shutdown; exit 130' INT
set +e
wait -n "$daemon" "$app"
status=$?
set -e
shutdown
# Unexpected clean child exit is still a supervisor failure.
(( status != 0 )) || status=1
exit "$status"
