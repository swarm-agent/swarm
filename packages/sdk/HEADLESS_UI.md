# Build a real headless UI with the SDK

## Trust boundary

Run the administrative Node SDK in your backend-for-frontend **inside the same container as swarmd**, using its configured private Unix socket. Do not mount/export that socket, put daemon credentials in browser code, or forward arbitrary browser-selected daemon paths. Authenticate your app's users, check Origin/CSRF, and allowlist app operations. Never log credential request bodies. The separate `--container-sdk-port` listener remains session-only: it cannot onboard providers, change settings, or create workspaces.

Node 22+ is required. `ws` supplies Unix-socket WebSocket upgrades; HTTP and WebSocket requests use the same SDK credentials/headers. Browser use requires a same-origin authenticated app proxy, no SDK bearer token; otherwise forward watcher updates from your BFF using your app's authenticated SSE/WebSocket transport.

## Discover, authenticate, configure

<copy>
import { SwarmClient } from '@swarmagent/sdk';
const swarm = new SwarmClient({ socketPath: process.env.SWARM_SOCKET_PATH });
const onboarding = await swarm.onboarding.get();
const providers = await swarm.settings.providers(); // readiness and supported auth_methods
const credentials = await swarm.auth.credentials.list(); // redacted status only
</copy>

Your app must supply its configured socket path and authenticated local identity. This is not a recipe to import host credentials. Complete the daemon's identity/onboarding requirements using `onboarding.update({ username, swarm_name })` when needed.

For an API key collected through your app UI, pass the user-selected provider and its advertised credential type. The first-provider endpoint requires zero existing credentials/agents; use the ordinary save endpoint afterward. Errors are not silently retried on a different route.

<copy>
await swarm.auth.credentials.save({
  provider: selectedProvider, type: selectedCredentialType,
  api_key: submittedKey, label: 'Headless app', active: true,
});
const catalog = await swarm.settings.models(selectedProvider);
const settings = await swarm.settings.agentModels();
// Display catalog.records and settings.agent_model_settings. Never invent a model fallback.
// Optional explicit user selection:
await swarm.settings.setSwarmModel('action', selectedAssignment);
</copy>

`auth.credentials` also exposes `verify(provider, id)`, `activate(provider, id)`, and `delete(provider, id)`. Verification may call a real provider. First-run OpenAI credentials may be stored active but unverified; inspect `connection` and `auto_defaults.error` rather than treating storage as successful inference.

### Codex sign-in

<copy>
const login = await swarm.auth.codex.start({ method: 'device', active: true });
// Display login.verification_url and login.user_code; never display access tokens.
// On explicit refresh, or a bounded UI login-status loop stopped at expiry/unmount:
const status = await swarm.auth.codex.status(login.session_id);
// status.status: waiting / authorizing / success / error
</copy>

Device login completes asynchronously in the daemon and is the preferred container flow. `manual` returns `auth_url`; submit the user's callback input with `auth.codex.complete(login.session_id, callbackInput)`. `browser` also returns `auth_url`, but its callback listener runs where the daemon runs, not automatically in the host browser's network namespace. Do not claim browser callback reachability without configuring and testing it. Device sessions reject `complete`. Login-status polling is separate from session realtime; chat must not poll.

## Create a workspace and durable session

<copy>
const folder = await swarm.workspaces.createFolder(configuredWorkspaceParent, 'my-app');
const repository = await swarm.workspaces.inspectRepository(folder.path);
if (repository.can_setup) {
  // Explicit user-approved Git initialization; expected path guards identity changes.
  await swarm.workspaces.setupRepository(folder.path, repository.path);
}
const workspace = await swarm.workspaces.add({ path: folder.path, make_current: false });
const session = await swarm.sessions.create({
  workspace_path: workspace.workspace_path, title: 'My app chat', mode: 'auto',
});
</copy>

Do not automatically commit existing content. `reviewRepository(path)` returns selectable paths and a review digest; `baselineRepository(...)` requires that digest, exact selected paths and explicit confirmations. `add` may return a repository prerequisite/content-review conflict. Surface it, do not silently set `confirm_committed_only`. Folder creation, Git preparation, registration and session creation are separate operations; failures can leave a created folder without a registered workspace.

## Stream without reverse engineering

<copy>
const abort = new AbortController();
const watch = swarm.realtime.watchSession(session.id, {
  signal: abort.signal,
  onChange(state) {
    // Replace the displayed snapshot. Key durable messages by id.
    // Render state.live separately by [runId, streamId]; it contains full text,
    // not append-only deltas. Committed streams disappear from this overlay.
    sendToAuthenticatedAppClient(state);
  },
  onError(error) { reportAppError(error.message); },
});
await watch.ready; // initial snapshot and scoped replay subscription established
// Keep the same client_request_id when retrying this submission.
await swarm.sessions.sendMessage(session.id, { content: userText, client_request_id });
await watch.done; // rejects on terminal transport/protocol/auth errors
// On app disconnect: watch.dispose() or abort.abort().
</copy>

The watcher hydrates one session (including its session view), waits for `hello` (`v3.realtime`, version 1), and resumes from the **snapshot handoff cursor**, not the newer hello head. The server replays to the subscription before delivering live updates. It advertises `live_patch_v1` only when the server offers it. UTF-8 byte offsets and sequence ranges deduplicate live text; durable checkpoints and committed messages remain authoritative. Foreign-session frames never update the view.

Durable events trigger coalesced `/v3/sync/hydrate` reads, not interval polling. Disconnects, cursor errors and slow consumers start fresh scoped hydration/replay with bounded exponential backoff (six retries, 250ms to 8s). Authentication/protocol errors terminate. No cursor is parsed, numerically compared, or persisted across watches/principals. `dispose` cancels HTTP, closes WebSocket, and ends `done`; a disposed-before-ready watcher rejects `ready`.

### Bounded view and raw access

The high-level view is a **200-message / 200-event tail**, not the complete transcript. Inspect `snapshot.pagination`, `omissions`, history manifests and session views before claiming complete history. A mid-stream reconnect whose retained event tail lacks the stream prefix may show no partial draft until a durable complete message arrives; it never guesses missing text. A live offset gap pauses that stream's speculative patches and relies on durable repair. Live overlays are bounded to 64 streams / 4 MiB per stream. Tool, plan, permission and other resources stay in the canonical raw snapshot; the SDK does not invent a second plan authority.

`realtime.bootstrap(request)`, `hydrate(request)` and `replay({...request, endpoint_cursor, limit})` expose typed canonical V3 envelopes for advanced consumers. Keep surface, selector and resources identical when using `/v3/sync/stream` cursors. `realtime.socket(path)` exposes transport only; raw consumers own reducer/replay correctness. The convenience watcher uses fresh durable repair on reconnect rather than retaining a possibly stale speculative cache.

No live daemon/provider or container validation is claimed by this guide. Unit fixtures prove wire/reducer behavior only; the deploying app must verify onboarding, real inference, streamed text, restart recovery and container isolation against its candidate.
