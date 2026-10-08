#!/bin/bash
set -euo pipefail
umask 077
config=/etc/swarmd/headless-app
mkdir -p "$config"
# The owner creates their login (account.json) in the app on first visit.
# Tailscale Serve (https://NAME.TAILNET.ts.net) terminates TLS on the host.
if [[ "${APP_ORIGIN:-http://127.0.0.1:8443}" == https:* && ! "${APP_ORIGIN}" =~ ^https://[a-z0-9.-]+\.ts\.net$ ]]; then
  [[ -s "$config/tls.key" && -s "$config/tls.crt" ]] || {
    echo 'Explicit HTTPS requires a trusted certificate and key in the config volume.' >&2; exit 1;
  }
fi
# Custom BFF commands are supplied as argv, never shell-evaluated.
if (( $# == 0 )); then set -- node /opt/workshop/examples/headless-app/server.mjs; fi
# Keep daemon/provider and application logs private, not on container stdout.
exec node /usr/local/share/swarm/supervisor.mjs "$@" >>/var/log/swarmd/headless-app.log 2>&1
