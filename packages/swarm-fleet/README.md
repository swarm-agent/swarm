# swarm-fleet

Swarm Control for every Swarm machine on your tailnet, as one stdio MCP
server. Claude Code (local, on the web, or in routines) starts it; it finds
the machines tagged `tag:swarm` on your tailnet, lists their Swarm tools with
a `machine` argument, and forwards each call to that machine's AI gateway.

```
Claude Code ──stdio──► swarm-fleet ──tailnet──► social  https://social.<tailnet>.ts.net:8444/mcp
                                          └──► vault   https://vault.<tailnet>.ts.net:8444/mcp
```

- Nothing is published to the internet. Each machine serves its gateway only
  on your tailnet (Tailscale Serve, port 8444).
- No keys: each machine admits this device by the `swarmagent.dev/cap/swarm`
  grant your Tailscale policy gives it (`read` or `write`). Approving tool
  calls and managing workers, limits or models are never granted this way.
  AI keys still work for a machine listed in `SWARM_FLEET` with a `token`.
- `swarm_fleet_status` is one view of every machine: reachable, access,
  running sessions and today's usage and limits.
- Off your tailnet (Claude Code on the web, CI), `fleet.sh` joins it as a
  throwaway node with `TS_AUTHKEY` (userspace networking, no privileges,
  state in memory, pinned and checksummed Tailscale) and keeps the key away
  from the MCP server process.

Setup, the Tailscale policy and the Claude Code on the web environment are in
[containers/headless/app/TAILNET.md](../../containers/headless/app/TAILNET.md).

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `FLEET_TAGS` | `tag:swarm` | comma-separated tags that mark Swarm machines |
| `FLEET_PORT` | `8444` | AI gateway port on each machine |
| `SWARM_FLEET` | empty | extra or overriding machines: `[{"name","url","token"?}]` |
| `FLEET_DISCOVER` | on | `off` uses only `SWARM_FLEET` |
| `TS_AUTHKEY` | | ephemeral, tagged auth key, only where the device is not on the tailnet |
| `FLEET_USERSPACE` | | `1` joins as a throwaway node even where Tailscale runs |
| `SWARM_FLEET_CA_BUNDLE` | `SSL_CERT_FILE` | CA file for Tailscale behind an inspecting proxy |

`fleet.sh --fetch-only` only downloads Tailscale; `fleet.sh --join-only`
joins and exits. The container (`Dockerfile`, `entrypoint.sh`) runs the same
server with Tailscale inside for clients that prefer Docker.

## Development

```sh
node --test test/*.test.mjs            # routing, discovery, fleet status, CONNECT + TLS path
sudo bash test/gateway-e2e.sh          # a real swarmd with tailnet identity behind a Serve stand-in
```
