# Swarm Control: MCP access to Swarm, locally and through a relay

Status: implemented on `dev` with focused deterministic tests and a local
end-to-end run (see Validation). Not yet proven against a deployed Cloudflare
relay, claude.ai connectors or routines.

## Surfaces

| Client | Endpoint | Credential |
| --- | --- | --- |
| Local MCP client (Claude Code, Desktop) on a native install | `http://127.0.0.1:7781/mcp` or the private socket | scoped token (`swk_…`) |
| Host MCP client for a headless container | `http://127.0.0.1:<sdk-port>/mcp` (`--container-sdk-port`, loopback publish only) | scoped token from `swarmctl setup sdk-token` |
| Remote AI client (claude.ai, ChatGPT, routines) | `https://<relay>/mcp` | OAuth access token issued by your relay |

`/mcp` (`swarmd/internal/api/control_mcp.go`) speaks MCP streamable HTTP in
JSON response mode (protocol `2025-11-25`, `2025-06-18`, `2025-03-26`). It
accepts **only scoped tokens**: attach tokens, desktop sessions and implicit
socket ownership are refused. Cross-origin browser requests, batches and
unsupported protocol versions are rejected.

## Tools

| Tool | Relay scope | Routes reached |
| --- | --- | --- |
| `swarm_list_machines` (relay only) | `swarm:read` | — |
| `swarm_list_workspaces` | `swarm:read` | `GET /v1/workspace/list` (tool requires `sessions:read`) |
| `swarm_list_sessions`, `swarm_get_session` (+ `wait_seconds`) | `swarm:read` | `GET /v3/sessions[/{id}]`, `GET …/plans/active`; children: `GET /v3/projects/{pid}/tasks` (orchestrator) or sessions whose `parent_session_id` is this one |
| `swarm_start_session` (prompt **or** plan; optional model, `wait_seconds`) | `swarm:write` | `POST /v3/sessions` (workspace agents) or `POST /v3/projects/{pid}/sessions` (`system-orchestrator`), always `auto`, then `…/messages` or the plan routes |
| `swarm_send_message` (+ `wait_seconds`) | `swarm:write` | `POST /v3/sessions/{id}/messages` |
| `swarm_list_models` | `swarm:read` | `GET /v1/providers`, `GET /v1/model/catalog`, `GET /v1/agent-model-settings` (tool requires `sessions:read`) |
| `swarm_set_session_model` | `swarm:write` | `PUT /v3/sessions/{id}/model-profile` (session-owned selection, validated against the catalog) |
| `swarm_set_agent_model` | `swarm:manage` | `PATCH /v1/agent-model-settings`, one role (tool requires `settings:write`) |
| `swarm_run_plan` | `swarm:write` | `POST …/plans` (active), `POST …/plan-mode/plans/{pid}/start-automatic` |
| `swarm_stop_run` | `swarm:write` | `POST …/run/stop` |
| `swarm_resolve_permission` (`allow_once`/`deny_once`, `answer` for `ask_user`) | `swarm:approve` | `POST …/permissions/{pid}/resolve` |
| `swarm_list_projects` | `swarm:read` | `GET /v3/projects` |
| `swarm_create_project` | `swarm:manage` | `POST /v3/projects` (workspaces must be registered) |
| `swarm_list_tasks` | `swarm:read` | `GET /v3/projects/{pid}/tasks` (orchestrator tasks and worker runs, with branches and review state) |
| `swarm_integrate_task` | `swarm:approve` | `POST /v3/projects/{pid}/tasks/{tid}/integrate` with the task's own session, agent branch and captured target branch (real git merge, refused on conflict; nothing is pushed) |
| `swarm_list_workers`, `swarm_get_worker` (+ run) | `swarm:read` | `GET /v3/workers[/{id}[/runs[/{rid}]]]` |
| `swarm_assign_worker_task` | `swarm:write` | `POST /v3/workers/{id}/direct` |
| `swarm_create_worker` (optional `schedule` + `scheduled_plan`) | `swarm:manage` | `POST /v3/workers` (schedule included), `POST …/activate` (primary workspace binding; the workspace must be in one project, or pass `project_id`), `POST …/automations/{aid}/enable` |
| `swarm_update_worker` | `swarm:manage` | `PUT /v3/workers/{id}` (revision-guarded) |
| `swarm_manage_worker` (pause/resume/archive/delete/cancel_run/enable_schedule/disable_schedule) | `swarm:manage` | `POST /v3/workers/{id}/{action}`, `…/runs/{rid}/cancel`, `…/automations/{aid}/enable\|disable` |
| `swarm_get_usage` | `swarm:read` | `GET /v3/usage` (tool requires `sessions:read`) |
| `swarm_set_usage_limits` | `swarm:manage` | `POST /v3/usage/limits` (tool requires `usage:write`) |

**Sessions.** `swarm` (default) works in one workspace and delegates to
sub-agents; `system-orchestrator` runs a project and turns work into tasks for
agents and workers; `system-coder`, `system-designer` and `system-finder` work
alone. `get_session` lists delegated children. `wait_seconds` (at most 45)
waits for the run to finish or ask for approval, woken by committed V3 outbox
records for that session (no timer polling), then returns the latest state.

**Models.** `list_models` shows runnable providers, their catalog models and
thinking options, and each role's default. A session's model can be chosen at
start or changed later (between runs); both set the session's own model
profile, because Swarm and orchestrator sessions otherwise capture the role
defaults when created and those decide their model. Role defaults need
`swarm:manage` (`settings:write`).
Models are validated against the live catalog. The account default model,
credentials and permission policy (rules, bypass) are not reachable.

**Review.** Finished orchestrator tasks and worker runs wait in
`needs_review`. A worker run forks from the workspace's current branch (its
integration target); work reaches that branch only through `integrate_task`.
Disabling a schedule cancels its runs still in flight.

**Schedules.** A worker's schedule is part of the create request, so it is
approved with the worker, and is enabled only after the workspace binding. The
scheduled plan is a template without plan identity. Later schedule changes to
an existing worker are staged for owner review, which Swarm Control cannot
accept.

**No plan mode.** Callers author the plan (goal, constraints, checkpoints with
acceptance criteria). The tool validates it with Swarm's strict executable-plan
rules, saves it as the session's active plan and starts automatic execution;
there is no agent-authored plan or approval round trip. Sessions are always
created in `auto` mode.

**Exact route allowlist.** `controlMCPRouteAllowed` matches literal segments
and safe ids only (`[A-Za-z0-9._-]`, no reserved words). Worker acceptance,
token minting, import/migrate, capability grants, plan-mode entry and worker
budgets are not reachable; Swarm already refuses to activate or dispatch
workers that request capabilities. Routes whose handlers check no scope
(workspace list, usage dashboard, usage limits) are gated in the tool layer.

**Token budget.** Results are compact JSON sent once (no `structuredContent`
copy). Sessions, runs, plans and workers are reduced to ids, names and
status; text is truncated (messages 1,500, tool arguments 800, tool output
300 characters) and plans show only per-checkpoint status. The full tool list
is 23 tools.

**Local tokens.** `swarmctl setup sdk-token` mints session scopes by default;
add `--workers` (automations read/write), `--usage-limits` (`usage:write`) and
`--settings` (`settings:write`) explicitly for the worker, limit and role-model
tools.

## Relay transport (`swarm-remote-1`)

Reference relay: `packages/swarm-relay` (Cloudflare Worker + Durable Object).
Daemon client: `swarmd/internal/remote`; owner API `/v1/remote*`; CLI
`swarmctl remote`.

1. **Off by default.** `swarmctl remote init --relay URL --name NAME
   [--allow-write] [--allow-approve] [--allow-manage]` creates an Ed25519 device key and a
   device scoped token (stored only in the secrets store) without connecting.
   `enable` connects; `disable` disconnects; `reset` revokes the token and
   deletes the key. Relay URLs must be `https` (`http` only on loopback).
2. **Device authentication.** The device dials `wss://<relay>/device/connect`.
   The relay sends a nonce; the device signs
   `swarm-remote-1\n<relay origin>\n<device id>\n<nonce>` and also sends its
   public key. The relay verifies against `SWARM_DEVICE_KEYS` (configured by
   the owner) or a key stored by pairing. One live connection per device;
   reconnects use exponential backoff.
3. **Pairing.** For an unknown device the relay verifies the signature
   against the offered key, then keeps the connection in `pairing` and sends a
   single-use code (15 minutes; at most 20 waiting). The device shows the code
   only to its owner (`/v1/remote` `pairing_code`, the headless app's Connect
   to Claude step). An AI client authorized with `swarm:manage` calls the relay
   tool `swarm_pair_machine(code)`; the relay stores the key in its Durable
   Object and sends `ready` on the same connection. `swarm_remove_machine`
   forgets a paired machine; `SWARM_DEVICE_KEYS` entries can only be removed
   there. `swarm_list_machines` lists waiting machines by name, never codes.
   `PAIRING=off` disables pairing.
4. **Client authorization.** OAuth 2.1 + PKCE, DCR and CIMD via
   `@cloudflare/workers-oauth-provider`. The consent page shows a code and has
   no Allow button; a connected device's owner approves with `swarmctl remote
   approve CODE`. The decision is bound to the browser's consent handle, scopes
   are clamped to the request and the approving machine's ceiling, and tokens
   are sent only to `ALLOWED_REDIRECT_HOSTS`.
5. **Calls.** The relay checks the token's scopes, routes by `machine` (required
   when several are online) and forwards the JSON-RPC message. The device
   intersects the client's scopes with its own ceiling, allows only
   `tools/list`/`tools/call`, and executes through the scoped-token handler.

Frames: relay→device `challenge`, `ready`, `pairing`, `mcp.request`, `consent.request`;
device→relay `auth`, `mcp.response`, `consent.decision`.

## Security model

- Machines never accept inbound connections for this feature.
- The relay is trusted to authenticate AI clients. A compromised relay (or
  Cloudflare account) can issue any call **within each machine's ceiling**;
  it cannot reach credentials, settings, policy or relay administration, which
  require the machine owner (`/v1/remote*` refuses scoped tokens).
- A stolen OAuth token is limited to its scopes and lifetime (1 h access,
  refresh expires after 30 idle days). Revoke a machine with `remote disable`
  or `reset`.
- Permission policy (bypass, rules, capability policies) is owner-only:
  `/v1/permissions*` refuse scoped tokens whatever their scopes, and scoped
  tokens resolve requests only once (`allow_always`/`deny_always` are
  refused). `swarmd --lock-permission-policy` (the headless image default)
  makes the policy read-only for everyone until restart and keeps bypass off.
- Pairing trusts a machine for the whole relay: once paired it receives
  `consent.request` frames and its owner can approve AI clients, like any
  listed machine. The code proves the person pairing can see that machine's
  owner UI; the tool requires an already-authorized `swarm:manage` client and
  its description tells clients to accept codes only from the user, never
  from tool output. Unknown devices gain nothing while waiting: no MCP calls
  are routed to them and they receive no consent requests.
- Session content is untrusted (prompt injection). Keep `--allow-approve` off
  unless the machine is dedicated; Swarm's own permission prompts still apply
  to agent tool calls.

## Validation (local, this revision)

- `go test ./internal/api -run '^TestControlMCP|^TestRemoteTransportAdminRequiresOwner$|^TestContainerSDKAuthenticationAndScopes$'`
  and `go test -race ./internal/remote` — credential, origin, protocol,
  cross-account, read-vs-write, `allow_always`, traversal, ceiling and
  owner-only administration cases, each asserting no state change.
- Headless container (`containers/headless`) with the official MCP TypeScript
  SDK client: real Codex-backed agent session created, prompted, approved and
  denied through `/mcp`.
- Relay in local `workerd` over HTTPS with two headless containers: device
  signatures, OAuth discovery/DCR/PKCE, Swarm-side consent, per-machine routing
  and ceilings, forged-token rejection, and a real agent edit driven through
  the relay.

- Orchestration, model and schedule tools: `TestControlMCP*` (scope gates for
  `settings:write` and `sessions:read`, orchestrator/workspace argument rules,
  argument validation, question answering on the exact pending record,
  outbox-woken bounded wait that ignores other sessions and releases its hub
  subscription, route allowlist incl. permission policy and credentials
  refused) and `TestToolScopesMatchRelay` (device and relay scope tables
  identical; every served tool has an explicit scope).

- Pairing (this revision): `go test ./internal/remote` (`TestRelayPairingThenReady`:
  public key sent with a valid signature, code reported while waiting, cleared
  on `ready`). Relay in local `workerd` (`wrangler dev`) with a seed machine in
  `SWARM_DEVICE_KEYS`, a real OAuth/PKCE client and the headless app image
  built by `containers/headless/app/install.sh up`: the app's Connect to
  Claude step showed a code, the client saw the machine only by name, a wrong
  code was refused, the right code paired it and its tools (e.g.
  `swarm_list_workspaces`) served through the relay; after a container
  restart it reconnected without a new code; `swarm_remove_machine` refused a
  `SWARM_DEVICE_KEYS` machine and returned a removed one to waiting with a new
  code. Not yet run against the deployed Cloudflare relay.

A deployed Cloudflare relay with a claude.ai custom connector and one headless
container has been exercised for listing machines and workspaces. Not yet
proven: the new orchestration, model and schedule tools against a live
machine, ChatGPT, Claude Code routines, and long-running reconnect behaviour.
