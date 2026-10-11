# One tailnet, every Swarm: AI access without keys or a public endpoint

Every Swarm machine serves its AI gateway (Swarm Control, an MCP server) only
on your tailnet, at `https://NAME.TAILNET.ts.net:8444/mcp`. Your Tailscale
policy decides which devices may use it and at what level. An AI client gets
the Swarm tools by running on a device in your tailnet: your laptop, or a
Claude Code on the web session that joins as a throwaway device.

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

- New machine: install with `TS_AUTHKEY=… bash install.sh --tag tag:swarm`
  (or open the login link and tag it afterwards). Tailnet identity is on when
  Tailscale is 1.92 or newer; the installer updates Tailscale if needed.
- Existing machine:
  1. Admin console → **Machines** → the machine → **Edit ACL tags** →
     `tag:swarm` (or `tag:swarm-protected` for read only from AI devices).
     The policy's `ssh` rules keep `tailscale ssh` working once it is tagged.
  2. On the machine:
     `sudo bash /var/lib/swarm-headless/src/containers/headless/app/install.sh tailnet-identity on`

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
