# Swarm Console

Swarm Console is a client on top of Swarm. Nothing else. Swarm
(this repository's `swarmd`) runs the agents and holds every record; this app shows them
and sends the person's actions. It is coded as a Swarm client, the way
Swarm's own web UI is one.

## The one rule

I will not bake MCP into the client. MCP is a path for other AIs. This is
a real app on top of the canonical Swarm.

A person plugs this app into a Swarm as a client, in a safe way: the way
Swarm's own clients connect. Never through MCP, never with an AI key.

- Local Swarm: the owner socket `<datadir>/local-transport/api.sock`
  (`/var/lib/swarmd/local-transport/api.sock` on a system install), then
  `GET /v1/auth/desktop/session` over it for a session token, sent as
  `X-Swarm-Token`. Same rights as the web UI, nothing more.
- Swarm on another machine: Swarm's own way for an admitted client
  (its Tailscale onboarding approval that admits an origin and issues the
  same session). Check `swarmd/internal/api/desktop_local_auth.go` and
  `desktop_boundary.go` before building; do not invent one.
- Forbidden, always: `/mcp`, `swk_` keys (`connect-app`, `connect-ai`,
  `/v3/auth/tokens`), the AI gateway on `:7783`, `/v3/activity` and
  `/v3/activity/stream`, the key files under `~/.config/swarm/apps`,
  SSH tunnels to any of those, and polling.

## How the app reads and acts

Same routes as Swarm's web UI (`web/src` at the repository root), no others:

- Snapshot: `POST /v3/sync/bootstrap`, `POST /v3/sync/hydrate`,
  `POST /v3/sessions:reconnect`.
- Live: WebSocket `/v3/realtime/stream?endpoint_cursor=…` (cursor from
  bootstrap); durable replay `/v3/sync/stream` is the correctness path.
  Frames: `hello`, `event`, `replay.*`, `keepalive`, `task.lifecycle.updated`,
  `project.updated`, `usage.scope.updated`, `auth.credentials.updated`,
  `auth.denied`, `slow_consumer.reconnect_required`
  (`swarmd/internal/api/sessions_v3_realtime_contract.go`).
- The Orchestrator inside the app is a Swarm session driven over HTTP:
  `POST /v3/sessions`, `POST /v3/sessions/{id}/messages`,
  `POST /v3/sessions/{id}/run/stop`,
  `POST /v3/sessions/{id}/permissions/{pid}/resolve`.
- Projects and tasks: `/v3/projects…`, `/v3/projects/{id}/tasks…`.
- Models, providers, sign-in: `/v1/agent-model-settings`, `/v1/providers`,
  `/v1/auth/codex/oauth/*`, `/v1/onboarding`.
- Usage: `/v3/usage`, `/v3/usage/limits`. Theme: `/v1/workspace/theme`.
- Media: `/v3/media-staging`, `/v3/sessions/{id}/media`,
  `/v3/projects/{id}/media`.
- Linking machines (fleet): `/v3/fleet…` HTTP routes on the home Swarm.
  The app shows what the home says and sends the person's actions there.

## Linking (fleet), remade

A home Swarm links other Swarms as a client of them: an owner-level
session on the machine, the same snapshot and live stream as above. Not a
scoped token, not a route allowlist, not the activity feed. The MCP fleet
tools are a side door for outside AIs only; neither this app nor Swarm
itself goes through them.

## One repository

The console lives in the Swarm repository, so a change that needs both sides
(a Swarm route and the screen that uses it) is one change. When the console
cannot do something because Swarm lacks it, fix Swarm (`swarmd`), never work
around it in the console. The repository's root `AGENTS.md` applies here too.
The console is released on its own schedule, separately from Swarm.

## Truth

- Swarm's records are the only source of truth. The app invents no state
  and keeps no copy that can disagree with Swarm.
- No invented limits (counts, sizes, durations).
- Say what is built, what is tested and what is not. Never say something
  was sent, done or passing without having seen it.

## Repo

- Rust, GPUI. `cargo run`; `cargo test`. Screens, panes, theme, icons,
  markdown and scripts (`scripts/`) are kept; everything that talks to Swarm
  is rewritten under the rules above. `src/swarm/control.rs` (MCP) is
  deleted, not adapted.
- Builds on its own from `console/` (`cargo build --release`); the Go build
  and Swarm's release do not include it. `target/` is never committed.
