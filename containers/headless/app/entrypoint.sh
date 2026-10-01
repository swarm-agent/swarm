#!/bin/bash
set -euo pipefail
umask 077
config=/etc/swarmd/headless-app
mkdir -p "$config"
if [[ ! -e "$config/login-secret" ]]; then
  openssl rand -hex 32 > "$config/login-secret"
fi
if [[ ! -e "$config/tls.key" && ! -e "$config/tls.crt" ]]; then
  openssl req -x509 -newkey rsa:3072 -nodes -days 365 \
    -subj '/CN=Swarm Local Workshop' -addext 'subjectAltName=IP:127.0.0.1' \
    -keyout "$config/tls.key" -out "$config/tls.crt" >/dev/null 2>&1
fi
[[ -s "$config/tls.key" && -s "$config/tls.crt" && -s "$config/login-secret" ]] || {
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
