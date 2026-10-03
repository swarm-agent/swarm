# Custom agent hub

A small, editable UI over `@swarm-agent/sdk`, not a new orchestration backend. Swap its
HTML/CSS for any experience. Application agents, context, conversations, project
links and worker links persist in Swarm. The browser keeps no authoritative state.

## Run against a configured private daemon

Build the SDK and install the example dependencies:

<copy>
npm --prefix packages/sdk ci
npm --prefix packages/sdk run build
npm --prefix examples/agent-hub ci
</copy>

Supply `SWARM_SOCKET_PATH` for a private same-host daemon connection, or
`SWARM_API_URL` and `SWARM_AUTH_TOKEN` for authenticated loopback transport.
Supply a random `APP_ACCESS_TOKEN` of at least 32 characters through your runtime's
secret configuration. Never put daemon or provider credentials in browser code.
The app token is access control, not Codex/provider sign-in. After unlocking, open
**Connect Codex** for user-driven device or manual sign-in. Consent is required:
OAuth completion can initialize account model defaults even though this example
requests `active: false` and never calls settings/activation APIs. Check status
explicitly; no background credential polling is used. OAuth IDs stay bound to the
app cookie, and only allowlisted status/HTTPS links/device codes reach the browser.
Forgetting a flow or locking the app does not revoke provider authorization already
granted: the daemon has no OAuth cancel endpoint. Manage shared credentials in
Swarm account settings. Account/workspace setup and model availability are still
prerequisites; this example does not silently configure them.

<copy>
npm --prefix examples/agent-hub start
</copy>

Open `http://127.0.0.1:8787`. On a cloud VM, keep both services private and access
through an approved private tunnel; **do not publish this port or the daemon socket**.
This example is not a publicly hosted multi-tenant service. No cloud resource is
created by these commands.

## What it demonstrates

1. Create a named agent with instructions/context; reload and discover it again.
2. Open a conversation in an existing authorized workspace; send messages or
   event text with stable retry IDs; reopen canonical history.
3. Link an existing project and submit/view tasks without requiring conversation UX.
4. Link existing workers and view configured automations/triggers and durable runs.

Task requests retain the canonical project admission/approval lifecycle. Worker
creation and activation stay with Orchestrator; existing automation configuration is submitted through canonical revision/review gates. The UI does not
publish social content or deploy workers. Instructions/context are pinned per
conversation; project and worker context remain independent and are not silently
synchronized. The UI labels this distinction.

## Limits and extending it

- The app streams conversations and linked task/worker results through authenticated,
  CSRF-protected POST streams. V3 durable notifications invalidate authorized reads;
  reconnect rehydrates state. Logout, expiry and disconnect dispose streams. Refresh
  reconnects an exhausted stream. No periodic polling is used.
- Resource pickers show the first 100 accessible resources; use the SDK pagination
  for larger catalogs. Initial worker plans/trigger schemas are authored in Orchestrator.
- Application-agent discovery is paginated. Conversation discovery exposes a
  bounded recent 1,000-session scan and indicates truncation; retained IDs reopen
  directly. No browser local-storage identity list is necessary.
- The browser request retry keys survive retries within the page, not page reload.
  External webhook adapters must durably retain their own delivery/event IDs and
  verify the sender before calling `apps.send`.
- App auth expires after eight hours and requires unlocking after server restart.
- No provider-backed/cloud or rendered-browser proof is claimed by unit tests.

<copy>
npm --prefix examples/agent-hub test
</copy>
