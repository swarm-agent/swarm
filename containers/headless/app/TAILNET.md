# One tailnet, every Swarm: AI access without keys or a public endpoint

Tailscale is optional. Swarm works on one machine without it: the installer
serves everything on 127.0.0.1 and `install.sh connect-ai` connects the AI on
that machine (see [README.md](README.md)). This page is for reaching Swarm
machines from your other devices.

A Swarm machine installed with `--tailnet` (or switched with
`install.sh tailnet on`) also serves its AI gateway (Swarm Control, an MCP
server) on your tailnet, at `https://NAME.TAILNET.ts.net:8444/mcp`. Your
Tailscale policy decides which devices may use it and at what level. An AI
client gets the Swarm tools by running on a device in your tailnet: your
laptop, or a Claude Code on the web session that joins as a throwaway device.

```
 your tailnet (private; nothing on the public internet)
 ┌──────────────────────────────────────────────────────────────────┐
 │ your laptop (member)       ── write ──► social   (tag:swarm)     │
 │ Claude Code on the web     ── read  ──► social   (tag:swarm)     │
 │   (tag:claude, ephemeral)  ── read  ──► vault    (tag:swarm-protected)
 └──────────────────────────────────────────────────────────────────┘
```

How a call is authenticated: the AI asks Claude Code to call a tool; Claude
Code sends the request over the tailnet; Tailscale on the Swarm machine knows
which device sent it (each device has its own key, set when it joined) and
Tailscale Serve attaches the `swarmagent.dev/cap/swarm` grant your policy
gives that device. Swarm reads only that. The AI never holds a credential.

| Level | Allows |
| --- | --- |
| `read` | list sessions, read a session, usage, models, projects, workers |
| `write` | also start sessions, send messages, run plans, stop runs |
| never | approve tool calls, manage workers, limits or models |

## 1. Tailscale policy (once)

Admin console → **Access controls**: paste
[`tailnet-policy.hujson`](tailnet-policy.hujson), keeping any sections you
already have. It replaces the default "everyone reaches everything" rule,
which would otherwise let throwaway AI devices reach every machine.

## 2. Each Swarm machine

- New machine: install with `TS_AUTHKEY=… bash install.sh --tailnet --tag tag:swarm`
  (or open the login link and tag it afterwards). A machine installed without
  Tailscale joins later with `sudo bash install.sh tailnet on --tag tag:swarm`
  (run as root with `TS_AUTHKEY` in the environment to skip the login link).
  Tailnet identity is on when Tailscale is 1.92 or newer; the installer
  updates Tailscale if needed.
- Existing machine:
  1. Admin console → **Machines** → the machine → **Edit ACL tags** →
     `tag:swarm` (or `tag:swarm-protected` for read only from AI devices).
     The policy's `ssh` rules keep `tailscale ssh` working once it is tagged.
  2. On the machine:
     `sudo bash /var/lib/swarm-headless/install.sh tailnet-identity on`

`install.sh status` shows the Serve entry; Swarm's log says
`tailnet identity on` when it is active. Turn it off with
`install.sh tailnet-identity off` (AI keys keep working either way).

## 3. Clients

**Claude Code on a device in your tailnet** (one machine):

```sh
claude mcp add --transport http swarm https://NAME.TAILNET.ts.net:8444/mcp
```

Every machine at once, with `swarm_fleet_status` for one view of all of them:

```sh
claude mcp add swarm -- bash /path/to/swarm/packages/swarm-fleet/fleet.sh
```

**Claude Code on the web**: in the environment's settings (cloud environment
menu → Edit):

- Environment variables:
  ```
  TS_AUTHKEY=<auth key>
  MCP_TIMEOUT=120000
  ```
  Create the key in the admin console → **Settings → Keys** → *Generate auth
  key*: **reusable**, **ephemeral**, **pre-approved**, tag **`tag:claude`**, with
  an expiry. Anyone who can use the environment can read its variables, so the
  key is only as strong as the `tag:claude` grants: keep them `read` unless you
  need `write`.
- Setup script: the contents of
  [`packages/swarm-fleet/claude-web-setup.sh`](../../../packages/swarm-fleet/claude-web-setup.sh).
- Network access must allow `pkgs.tailscale.com`, `*.tailscale.com` and
  `raw.githubusercontent.com`.

A new session then has the `swarm` tools: ask it to "list my Swarm machines".

## Turning things off

- One AI device: Machines → remove it (ephemeral ones vanish on their own).
- All AI devices: revoke the auth key, or delete the `tag:claude` grants.
- One machine: `install.sh tailnet-identity off`, or remove its tag.

## Provider keys without an SSH tunnel

A write connection can ask `swarm_request_provider_key` for a one-time page.
When the tailnet identity gateway is active, Swarm reads this machine's
`Self.DNSName` from `tailscale status --json` and uses
`https://MACHINE.TAILNET.ts.net:8444/keys/CODE`. If the gateway/name is unavailable,
the existing loopback link remains available. Serve 1.92+ and a user-device
`Tailscale-User-Login` header are required on the gateway; tagged devices without
a user login cannot open this human entry page. The browser needs tailnet network
access, not the Swarm app capability grant. The gateway audits login and outcome,
not the key or capability URL.

For several machines, ask swarm-fleet for `swarm_fleet_request_provider_key` with
`provider`, explicit `machines: [names]`, and optional `label`. Open its single
link on a device in the same tailnet, check the machine list, and paste once.
The browser submits directly to each machine; no daemon forwards the key and
neither swarm-fleet nor the AI sees it. The additional URLs are in a browser-only
fragment. CORS permits only HTTPS port 8444 gateways in the same tailnet with a
pending capability. The page shows each result; the AI follows up using
`swarm_fleet_wait_provider_key` (provider, machines, wait_seconds up to 300).

Each link expires in ten minutes, allows five failed attempts and is single-use
on success. Partial fleet failure does not undo successful saves: ask for a new
link for failed machines only. Never paste keys into chat. Loopback entry still
works locally; relay clients never receive the request-key tool.
