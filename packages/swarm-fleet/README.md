# swarm-fleet

Swarm Control for every Swarm machine on your tailnet, as one stdio MCP
server. Claude Code (local, on the web, or in routines) starts it; it lists the
Swarm tools of all your machines with a `machine` argument and forwards each
call to that machine's AI gateway with that machine's AI key.

```
Claude Code ──stdio──► swarm-fleet container ──tailnet──► machine A  https://a.<tailnet>.ts.net:8444/mcp
                         (userspace Tailscale)        └──► machine B  https://b.<tailnet>.ts.net:8444/mcp
```

- Nothing is published to the internet. Each machine serves its gateway only
  on your tailnet (Tailscale Serve, port 8444), and only to AI keys.
- Each machine enforces its own key: **read only** keys see and call read
  tools; **read and write** keys may also start sessions, send messages and
  stop runs. No AI key may approve tool calls or manage workers, limits or
  models, and AI keys are refused on every route except `/mcp`.
- The container joins your tailnet as a throwaway node (in-memory state,
  userspace networking, no privileges) and logs out when the client closes it.
  The Tailscale auth key is not passed to the MCP server process.

## On each machine

Install with `containers/headless/app/install.sh` (it serves the gateway on
port 8444 unless `--no-ai-access`), open the app, and under **AI access over
Tailscale** create a key. The page shows the address, the key once, the
Tailscale access rule and the client settings.

## Tailscale

1. Access controls: paste the rule the app shows. It keeps your own devices'
   access and lets devices tagged `tag:claude` reach only the gateway port.
2. Settings → Keys: create an auth key that is **reusable**, **ephemeral**,
   **pre-approved** and tagged **`tag:claude`**.

## Clients

**Claude Code on a device in your tailnet** needs no container:

```sh
claude mcp add --transport http swarm https://<machine>.<tailnet>.ts.net:8444/mcp \
  --header "Authorization: Bearer <AI key>"
```

**Claude Code on the web and routines** run `fleet.sh` from the repository's
`.mcp.json`:

```json
{ "mcpServers": { "swarm": { "command": "bash", "args": ["fleet/fleet.sh"] } } }
```

with these environment variables in the cloud environment's settings:

```
SWARM_FLEET=[{"name":"box","url":"https://box.<tailnet>.ts.net:8444/mcp","token":"<AI key>"}]
TS_AUTHKEY=<reusable, ephemeral, tag:claude auth key>
MCP_TIMEOUT=120000
```

and this setup script, which starts Docker and builds the image ahead of the
first session (`MCP_TIMEOUT` covers a first build that happens later):

```sh
#!/bin/bash
if ! docker info >/dev/null 2>&1; then
  (setsid dockerd >/var/log/dockerd.log 2>&1 &)
  for _ in $(seq 1 30); do docker info >/dev/null 2>&1 && break; sleep 1; done
fi
# Prebuild from the cloned repository when it is already present.
for dockerfile in $(find /home -maxdepth 3 -path '*/fleet/Dockerfile' 2>/dev/null); do
  docker build -q -t swarm-fleet:local "$(dirname "$dockerfile")" >/dev/null 2>&1 || true
done
exit 0
```

Claude Code on the web loads a repository's `.mcp.json` only in a session with
that one repository. Environment variables are visible to everyone who uses
the environment: keep keys read only unless you need more, and give them short
lifetimes.

## Development

```sh
node --test test/*.test.mjs     # fleet routing, key validation, CONNECT + TLS path
docker build -t swarm-fleet:local .
```
