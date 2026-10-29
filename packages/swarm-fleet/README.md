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
  `running_now` (agent runs running there now), running sessions and today's
  usage and limits (`sessions_today`: sessions that used a model today).
- Off your tailnet (Claude Code on the web, CI), `fleet.sh` joins it as a
  throwaway node with `TS_AUTHKEY` (userspace networking, no privileges,
  state in memory, pinned and checksummed Tailscale) and keeps the key away
  from the MCP server process.

Setup, the Tailscale policy and the Claude Code on the web environment are in
[containers/headless/app/TAILNET.md](../../containers/headless/app/TAILNET.md).

## One model table for the fleet

Every machine runs the same agent model table: one `provider`, `model` and
`thinking` per role (`coder`, `finder`, `designer`, `compact`, `router`; the
advanced `action` and `plan` rows are the orchestrating agents and rarely
worth changing). A machine differs only by an explicit override, which is the
exception: the point is delegating the same way everywhere, not tuning each
box. Two tools carry it:

- `swarm_fleet_models`: the table, the overrides, and per machine what each
  role actually runs: `in_sync`, `override`, `drift` (differs from the table,
  with `wanted`), `unset` (nothing in the table) or `unknown` (unreachable).
- `swarm_fleet_set_models`: `{models: {coder: {provider: "<provider>",
  model: "<model>", thinking?: "<level>"}}}` merges into the table and applies
  it to every machine, one `swarm_set_agent_model` per role per machine;
  `{machine, models}` sets an override for that machine only; `{machine,
  clear_override: true}` drops it and applies the table there again;
  `apply: false` only records. Leave `thinking` out to use each machine's
  catalog default. Machines that refuse or cannot be reached are listed in
  `applied` with `ok: false`.

The table is the server's only state: `SWARM_FLEET_STATE` (default
`$XDG_CONFIG_HOME/swarm-fleet/fleet.json`, else `~/.config/swarm-fleet/fleet.json`),
mode 0600, no secrets in it. Setting models is a `manage` operation on the
machine: a machine admitted only by the tailnet grant (read or write) is
shown but cannot be changed; give it a full AI key as `token` in
`SWARM_FLEET`.

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `FLEET_TAGS` | `tag:swarm` | comma-separated tags that mark Swarm machines |
| `FLEET_PORT` | `8444` | AI gateway port on each machine |
| `SWARM_FLEET` | empty | extra or overriding machines: `[{"name","url","token"?}]` |
| `FLEET_DISCOVER` | on | `off` uses only `SWARM_FLEET` |
| `SWARM_FLEET_STATE` | `~/.config/swarm-fleet/fleet.json` | the fleet model table (see above) |
| `TS_AUTHKEY` | | ephemeral, tagged auth key, only where the device is not on the tailnet |
| `FLEET_USERSPACE` | | `1` joins as a throwaway node even where Tailscale runs |
| `SWARM_FLEET_CA_BUNDLE` | `SSL_CERT_FILE` | CA file for Tailscale behind an inspecting proxy |

`fleet.sh --fetch-only` only downloads Tailscale; `fleet.sh --join-only`
joins and exits. The container (`Dockerfile`, `entrypoint.sh`) runs the same
server with Tailscale inside for clients that prefer Docker.

## Development

```sh
node --test test/*.test.mjs            # routing, discovery, fleet status, the model table, CONNECT + TLS path
sudo bash test/gateway-e2e.sh          # a real swarmd with tailnet identity behind a Serve stand-in
```

## Add a provider key across the fleet

Ask the AI to call `swarm_fleet_request_provider_key` with `provider`, explicit
`machines: [names]`, and optionally `label`. Write access is enough (read and
relay clients cannot request these links). Open the returned link on a **user
device in your tailnet**, check the listed machines, and paste the key once.
The device needs network access to each gateway on HTTPS port 8444, but does not
need the `swarmagent.dev/cap/swarm` grant. Tailscale Serve supplies its user login.

Each machine verifies and activates its own credential. Your browser sends the
key directly to each machine; neither the AI nor swarm-fleet receives it. Other
page URLs live in the `#also=` fragment, not an HTTP request. Only gateways in
the same tailnet are accepted. Links expire after ten minutes, allow five failed
attempts and are consumed on success. Keep the link private too.

This is a two-step flow:

1. The tool returns as soon as every machine issues its page, with `url`,
   `tell_the_person`, `expires_in_seconds`, and each machine's `waiting` status.
   The AI gives the person the link and repeats `tell_the_person`, which names
   the destinations so they can match them against the hostnames on the page.
   No MCP notification or progress message is needed or sent.
2. Call `swarm_list_providers` with `machine`, `provider`, and `wait_seconds`
   (at most **110**) for each named machine to wait for its outcome.
   If still waiting, call again; the initial request does not wait for saves.

Failures are not atomic across machines: keep successful results and request a
new link only for failed machines. A restart forgets outstanding links.
