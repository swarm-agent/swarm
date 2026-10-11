#!/usr/bin/env bash
# Tailnet identity end to end, without a tailnet:
#
#   sudo bash packages/swarm-fleet/test/gateway-e2e.sh
#
# Builds swarmd and swarmctl, runs the daemon as a non-root user with the AI
# gateway on 127.0.0.1 and --tailnet-identity, and puts a stand-in for
# Tailscale Serve in front of it: like Serve, it drops any
# Tailscale-App-Capabilities header the caller sends and sets the grant
# configured for the "device". swarm-fleet then talks to the gateway through
# it with no key. Checks: a read grant lists and calls only read tools, a
# write grant may start sessions, no grant is refused, a forged header is
# dropped, and an old Tailscale turns tailnet identity off.
#
# Needs: root, Go, Node. Disposable state only.
# shellcheck disable=SC2034  # $out is read by the checks through eval
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
user=${GATEWAY_TEST_USER:-swarm-gw-test}
sdk_port=${SDK_PORT:-7783}
serve_port=${SERVE_PORT:-18444}
node=$(command -v node)
[[ $EUID -eq 0 ]] || { echo "run as root" >&2; exit 1; }

work=$(mktemp -d)
daemon_pid='' serve_pid=''
cleanup() {
  [[ -n $serve_pid ]] && kill "$serve_pid" 2>/dev/null || true
  [[ -n $daemon_pid ]] && kill "$daemon_pid" 2>/dev/null && wait "$daemon_pid" 2>/dev/null || true
  [[ -n ${KEEP_WORK:-} ]] && echo "kept $work" || rm -rf "$work"
}
trap cleanup EXIT
id "$user" >/dev/null 2>&1 || useradd --system --user-group --no-create-home --shell /usr/sbin/nologin "$user"

echo "Building swarmd and swarmctl"
mkdir -p "$work/bin" "$work/lib"
(cd "$repo/swarmd" && for c in swarmd swarmctl swarm-fff-search; do go build -o "$work/bin/$c" "./cmd/$c"; done)
cp "$repo/swarmd/internal/fff/lib/linux-amd64-gnu/libfff_c.so" "$work/lib/"
home=$work/home project=$work/project state=$work/state
mkdir -p "$home" "$project" "$state"/{data,cache,run,config,logs}
git -C "$project" init -q -b main
printf 'a\n' >"$project/README"
git -C "$project" add README
git -C "$project" -c user.name=test -c user.email=test@example.invalid commit -q -m init
chown -R "$user:$user" "$work"
chmod 0711 "$work"

fake_tailscale() { printf '#!/bin/sh\necho "%s"\necho "  tailscale commit: test"\n' "$1" >"$work/bin/tailscale"; chmod 0755 "$work/bin/tailscale"; }
daemon_env=(HOME="$home" PATH="$work/bin:/usr/bin:/bin" LD_LIBRARY_PATH="$work/lib"
  STATE_DIRECTORY="$state/data" CACHE_DIRECTORY="$state/cache" RUNTIME_DIRECTORY="$state/run"
  CONFIGURATION_DIRECTORY="$state/config" LOGS_DIRECTORY="$state/logs" SWARM_DISABLE_MINT_REPORT=1)
as_daemon() { runuser -u "$user" -- env -i "${daemon_env[@]}" SWARMD_LOCAL_TRANSPORT_SOCKET="$state/data/local-transport/api.sock" "$@"; }
start_daemon() {
  setpriv --reuid="$user" --regid="$user" --init-groups env -i "${daemon_env[@]}" \
    "$work/bin/swarmd" --desktop-port=0 --cwd="$project" --sandbox=off \
    --container-sdk-port="$sdk_port" --container-sdk-host=127.0.0.1 --tailnet-identity >>"$work/daemon.log" 2>&1 &
  daemon_pid=$!
  for _ in $(seq 1 60); do as_daemon swarmctl setup status >/dev/null 2>&1 && return 0; sleep 1; done
  echo "daemon did not start:" >&2; tail -n 40 "$work/daemon.log" >&2; exit 1
}
stop_daemon() { kill "$daemon_pid"; wait "$daemon_pid" 2>/dev/null || true; daemon_pid=''; }

# The Serve stand-in: the grant for the calling "device" is read from a file
# so the test can change it between calls.
cat >"$work/serve.mjs" <<'JS'
import { createServer, request } from 'node:http';
import { readFileSync } from 'node:fs';
const [listen, upstream, grantFile] = process.argv.slice(2);
createServer((req, res) => {
  const headers = { ...req.headers };
  delete headers['tailscale-app-capabilities'];
  const grant = readFileSync(grantFile, 'utf8').trim();
  if (grant) headers['tailscale-app-capabilities'] = grant;
  headers['x-forwarded-for'] = '100.64.0.7';
  const up = request({ host: '127.0.0.1', port: Number(upstream), method: req.method, path: req.url, headers }, r => { res.writeHead(r.statusCode, r.headers); r.pipe(res); });
  up.on('error', e => { res.writeHead(502); res.end(String(e)); });
  req.pipe(up);
}).listen(Number(listen), '127.0.0.1');
JS
grant_file=$work/grant
grant() { printf '%s' "$1" >"$grant_file"; }
grant ''
"$node" "$work/serve.mjs" "$serve_port" "$sdk_port" "$grant_file" &
serve_pid=$!

# One fleet session: send JSON-RPC lines, print the replies.
fleet() {
  printf '%s\n' "$@" | SWARM_FLEET="[{\"name\":\"local\",\"url\":\"http://127.0.0.1:$serve_port/mcp\"}]" FLEET_DISCOVER=off \
    timeout 60 "$node" "$repo/packages/swarm-fleet/server.mjs" 2>/dev/null
}
list='{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
status_call='{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"swarm_fleet_status"}}'
start_call='{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"swarm_start_session","arguments":{"workspace_path":"'"$project"'","prompt":"hello"}}}'
direct() { curl -sS -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' ${2:+-H "$2"} --data "$list" "http://127.0.0.1:$1/mcp"; }

failures=0
check() { if eval "$2"; then echo "  ok   $1"; else echo "  FAIL $1"; failures=$((failures + 1)); fi; }

fake_tailscale 1.104.1
start_daemon
as_daemon swarmctl setup identity --username owner --name gateway-test >/dev/null
as_daemon swarmctl setup workspace --path "$project" --name Project >/dev/null
printf 'placeholder-key\n' | as_daemon swarmctl setup credential --provider codex --api-key-stdin >/dev/null
as_daemon swarmctl setup complete >/dev/null
for _ in $(seq 1 30); do curl -s -o /dev/null "http://127.0.0.1:$serve_port/" && break; sleep 0.3; done

echo "Tailscale 1.104.1, tailnet identity on"
check "daemon log says tailnet identity is on" "grep -q 'tailnet identity on' '$work/daemon.log'"
grant ''
check "no grant: refused" "[[ \$(direct $serve_port) == 401 ]]"
grant '{"swarmagent.dev/cap/swarm":[{"level":"read"}]}'
out=$(fleet "$list" "$status_call" "$start_call")
check "read grant: read tools listed" "grep -q swarm_list_sessions <<<\"\$out\""
check "read grant: write tools hidden" "! grep '\"id\":1,' <<<\"\$out\" | grep -q swarm_start_session"
check "read grant: fleet status reaches the machine" "grep '\"id\":2,' <<<\"\$out\" | grep -q 'read only'"
check "read grant: start session refused" "grep '\"id\":3,' <<<\"\$out\" | grep -q 'limited to swarm:read'"
grant '{"swarmagent.dev/cap/swarm":[{"level":"write"}]}'
out=$(fleet "$list" "$start_call")
check "write grant: write tools listed" "grep '\"id\":1,' <<<\"\$out\" | grep -q swarm_start_session"
check "write grant: session started" "grep '\"id\":3,' <<<\"\$out\" | grep -q 'session' && ! grep '\"id\":3,' <<<\"\$out\" | grep -q '\"isError\":true'"
grant '{"swarmagent.dev/cap/swarm":[{"level":"approve"},{"level":"manage"}]}'
check "approve/manage grant: refused" "[[ \$(direct $serve_port) == 403 ]]"
grant ''
check "forged header through Serve: dropped" "[[ \$(direct $serve_port 'Tailscale-App-Capabilities: {\"swarmagent.dev/cap/swarm\":[{\"level\":\"write\"}]}') == 401 ]]"
grep -q 'swarmd tailnet gateway: 100.64.0.7 level=write' "$work/daemon.log" && echo "  ok   calls are logged with the device address" || { echo "  FAIL device not logged"; failures=$((failures + 1)); }
stop_daemon

echo "Tailscale 1.80.0, tailnet identity refused"
fake_tailscale 1.80.0
start_daemon
grant '{"swarmagent.dev/cap/swarm":[{"level":"write"}]}'
check "daemon log says tailnet identity is off" "grep -q 'tailnet identity off' '$work/daemon.log'"
check "grant ignored: bearer token required" "[[ \$(direct $serve_port) == 401 ]]"
stop_daemon

echo
if ((failures)); then echo "$failures check(s) failed; daemon log: $work/daemon.log"; KEEP_WORK=1; exit 1; fi
echo "All tailnet identity checks passed."
