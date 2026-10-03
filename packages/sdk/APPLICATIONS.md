# Application agents

`client.apps` stores named, account-and-user-owned application instructions/context.
It is **not** a mutable system-agent profile, worker, deployment or new executor.
Models, tools, permissions and worktree admission still use Swarm's canonical authorities.

Use an authenticated private BFF connection to the daemon local API (Unix socket
or authenticated loopback). These routes are deliberately **not** allowed on the
container SDK listener; do not expose the privileged socket or credentials to a browser.
The routes require `sessions:read` / `sessions:write`, respectively.

## Minimal private-server quickstart

First construct `new SwarmClient()` in your BFF using its private environment/socket
transport (never in the browser). If Codex is not connected, after explicit user
consent call `client.auth.codex.start({method: 'device', active: false})`, display
only the allowlisted HTTPS verification URL/user code, and call
`client.auth.codex.status(login.session_id)` on user request until `status === 'success'`.
Bind the login ID to your authenticated app session. Manual sign-in uses
`start({method: 'manual', active: false})` then `complete(login.session_id, callback)`.
Completion saves credentials and **can initialize account model defaults**, even
with `active: false`; disclose this before starting. The current API has no cancel
endpoint: abandoning a device flow does not revoke authorization already granted.
These are user-driven calls, not startup hooks. App access tokens and provider
sign-in are separate. Use the hub's BFF as a redaction/correlation example.

With a saved workspace and configured account model:

```ts
import { SwarmClient } from '@swarmagent/sdk';
const client = new SwarmClient();
const agent = await client.apps.put('editor', {
  name: 'Editor', instructions: 'Prepare drafts; do not publish.',
  context: 'Use our approved style guide.', expected_revision: 0,
});
const conversation = await client.apps.createConversation(agent.id, {
  workspace_id: savedWorkspaceId, revision: agent.revision,
  client_request_id: 'conversation-opening-1', title: 'Draft review',
});
await client.apps.send(agent.id, conversation.id, {
  content: 'Prepare a launch announcement.', client_request_id: 'event-1',
});
// Persist these IDs in the application; reopen after reload or daemon restart.
const result = await client.apps.conversation(agent.id, conversation.id);
```

`PUT /v3/application-agents/{id}` creates at expected revision zero or updates at
an exact revision; an identical immediately retried write returns the same record.
Stale/different writes return 409. `GET` returns latest context. Each conversation
pins an immutable revision at creation. Updates affect **new** conversations only;
they never silently rewrite an existing conversation's instructions. Use the same
request ID and revision when retrying creation or delivering the same event.
Instructions reach the V3 provider instruction boundary, not synthetic chat messages.
Conversation/message/event routes verify both principal and server-stored binding.
`apps.list({limit, cursor})` discovers persisted agents (limit 1–100). Use the returned
`next_cursor` unchanged. `apps.conversations(id)` returns a bounded recent view,
explicitly reporting when its 1,000-session scan limit is reached; retained IDs
always reopen directly. There is no delete API in this initial contract.

## Task-oriented applications remain independent

No conversation UX is required for the existing project task or worker APIs:

```ts
const queued = await client.projects.createTask(projectId, {
  agent: 'swarm', title: 'Prepare release notes', description: 'Summarize approved changes.',
});
// Use the returned task ID with the existing approval/reopen lifecycle.
const result = await client.projects.getTask(projectId, taskId);
const worker = await client.workers.get(workerId);
```

Optionally include `project_id` and `worker_ids` in `apps.put`. The server validates
account ownership and resource-read scopes before persisting these links. These
links are navigation/execution targets, not context synchronization:

```ts
await client.apps.createTask(agent.id, { id: durableJobId, agent: 'swarm', title: 'Draft', description: brief });
const tasks = await client.apps.tasks(agent.id);
const worker = await client.apps.worker(agent.id, linkedWorkerId);
const runs = await client.apps.runs(agent.id, linkedWorkerId);
```

Task creation requires an explicit `agent` or `intent`; `agent: 'swarm'` selects
ordinary general work, not a model/provider override. The linked project must have
an authorized repository workspace resolvable by canonical source admission.
Reuse the same durable task ID and payload on retry. Creation does not request
auto-approval; retain the existing approval lifecycle.

Projects, tasks and workers retain their own server-side context, revision,
trigger and result contracts. Task requests go through canonical project admission;
linked configuration uses `apps.configureAutomation(id, workerId, revision, input, automationId?)`;
`apps.trigger(id, {worker_id, automation_id, payload, idempotency_key})` preserves delivery identity.
Unlinked worker IDs are rejected. Canonical worker scopes, revisions and approval gates remain enforced;
activation, deployment, enable/disable and token minting are deliberately not forwarded.
The editable `examples/agent-hub` UI demonstrates these calls.
Task execution may have internal durable sessions without requiring chat in your UX.
Worker deployment/automation management is an Orchestrator operation. These snippets
are API usage examples, not a claim of a live deployed or cloud-verified workflow.

## Automatic results without a chat UI

<copy>
const results = client.apps.watchResults(agent.id, {
  onChange: ({ tasks, workers }) => renderResults(tasks, workers),
  onError: () => showReconnectButton(),
});
await results.ready;
// On logout, navigation or component disposal:
results.dispose();
await results.done;

const chat = await client.apps.watchConversation(agent.id, conversation.id, {
  onChange: ({ messages, live }) => renderConversation(messages, live),
});
</copy>

Watchers use the existing authenticated V3 socket, opaque snapshot resume cursors,
authorized resource reads and bounded reconnects. No polling or new execution state
is introduced. The resource watcher uses the daemon's account-scoped, content-free
worker invalidation signals; project invalidations are filtered by the linked ID.
It returns bounded task/run lists, not an unlimited event ledger. Disconnects re-read
durable state, not transient event payloads. Browser apps should stream these selected
results through their authenticated BFF; never send daemon credentials to the browser.

Resource-only resume currently has no explicit acceptance/replay-done frame.
`watchResults.ready` waits for a post-hello resource/watermark/keepalive and a fresh
authorized snapshot (idle connections can take one 15-second keepalive interval).
This is a liveness fence, not an atomic snapshot of all resources. Read-time
invalidations coalesce into another read; reconnect bootstraps again.
Worker signals currently omit worker IDs, so **every account worker invalidation
requires refreshing linked workers**; unrelated worker events cannot safely be
filtered client-side. Per-worker invalidation needs a backend protocol change.
