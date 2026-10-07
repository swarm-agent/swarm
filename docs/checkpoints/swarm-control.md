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

| Tool | Scope (relay) | API route reached |
| --- | --- | --- |
| `swarm_list_machines` (relay only) | `swarm:read` | — |
| `swarm_list_sessions` | `swarm:read` | `GET /v3/sessions` |
| `swarm_get_session` | `swarm:read` | `GET /v3/sessions/{id}` |
| `swarm_create_session` | `swarm:write` | `POST /v3/sessions` |
| `swarm_send_message` | `swarm:write` | `POST /v3/sessions/{id}/messages` |
| `swarm_stop_run` | `swarm:write` | `POST /v3/sessions/{id}/run/stop` |
| `swarm_resolve_permission` (`allow_once`/`deny_once` only) | `swarm:approve` | `POST /v3/sessions/{id}/permissions/{pid}/resolve` |

Each tool makes exactly one request through the container SDK route allowlist
under the caller's verified identity, so canonical V3 scope, ownership,
idempotency and permission checks decide. Persistent allow/deny rules,
credentials, settings and bypass are not reachable. Session reads are bounded:
long text is truncated and tool results are summarized.

## Relay transport (`swarm-remote-1`)

Reference relay: `packages/swarm-relay` (Cloudflare Worker + Durable Object).
Daemon client: `swarmd/internal/remote`; owner API `/v1/remote*`; CLI
`swarmctl remote`.

1. **Off by default.** `swarmctl remote init --relay URL --name NAME
   [--allow-write] [--allow-approve]` creates an Ed25519 device key and a
   device scoped token (stored only in the secrets store) without connecting.
   `enable` connects; `disable` disconnects; `reset` revokes the token and
   deletes the key. Relay URLs must be `https` (`http` only on loopback).
2. **Device authentication.** The device dials `wss://<relay>/device/connect`.
   The relay sends a nonce; the device signs
   `swarm-remote-1\n<relay origin>\n<device id>\n<nonce>`. The relay verifies
   against `SWARM_DEVICE_KEYS`, configured by the owner at deploy time. One live
   connection per device; reconnects use exponential backoff.
3. **Client authorization.** OAuth 2.1 + PKCE, DCR and CIMD via
   `@cloudflare/workers-oauth-provider`. The consent page shows a code and has
   no Allow button; a connected device's owner approves with `swarmctl remote
   approve CODE`. The decision is bound to the browser's consent handle, scopes
   are clamped to the request and the approving machine's ceiling, and tokens
   are sent only to `ALLOWED_REDIRECT_HOSTS`.
4. **Calls.** The relay checks the token's scopes, routes by `machine` (required
   when several are online) and forwards the JSON-RPC message. The device
   intersects the client's scopes with its own ceiling, allows only
   `tools/list`/`tools/call`, and executes through the scoped-token handler.

Frames: relay→device `challenge`, `ready`, `mcp.request`, `consent.request`;
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

Not yet proven: deployed Cloudflare relay, claude.ai custom connectors,
ChatGPT, Claude Code routines, and long-running reconnect behaviour.
