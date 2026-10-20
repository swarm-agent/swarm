# @swarm-agent/sdk

Official TypeScript SDK for [Swarm](https://github.com/swarm-agent/swarm), the local-first AI coding workspace.

`@swarm-agent/sdk` provides a typed client to interact programmatically with the Swarm daemon (`swarmd`) over HTTP and its local owner socket.

## Features

- **Finds your local Swarm**: In Node, `new SwarmClient()` connects to the owner socket of a system install (`/var/lib/swarmd/local-transport/api.sock`) when the process may use it, otherwise to `http://127.0.0.1:7781`.
- **Owner Socket**: The owner socket is trusted daemon-owner IPC: run as the user that runs Swarm and no token is needed.
- **Granular Scoped Tokens**: Create, list, revoke, and purge scoped deploy tokens (`swk_...`) with fine-grained permissions (`automations:trigger`, `sessions:read`, `sessions:write`, `admin`).
- **Worker / Automation V2 Triggers**: Trigger workers on-demand with dynamic caller context (e.g. GitHub Webhooks, CI/CD events, alert payloads).
- **Session & Workspace Lifecycle**: Create durable V3 sessions, manage runs, send messages, poll for completion, and inspect workspaces.
- **Typed Error Hierarchy**: `SwarmAuthError` (401), `SwarmForbiddenError` (403), `SwarmNotFoundError` (404), `SwarmConflictError` (409), `SwarmTimeoutError`.

## Install

Requires Node 22 or newer (ESM). The package is public on npm:

<copy>
npm install @swarm-agent/sdk
</copy>

You need a running Swarm. Install the Swarm box (Ubuntu 24.04 x86_64) with
`curl -fsSL https://swarmagent.dev/install | sudo bash`.

## Connect to your Swarm

`new SwarmClient()` with no options picks the first of:

1. `socketPath` or `baseUrl` passed to the constructor;
2. `SWARM_SOCKET_PATH` (a Unix socket);
3. `SWARM_API_URL` (or `SWARM_DESKTOP_URL`);
4. Node only: the owner socket `/var/lib/swarmd/local-transport/api.sock`, if it
   exists, is a socket and this process can read and write it;
5. `http://127.0.0.1:7781`, the daemon's default API address.

The owner socket's directory is private to the user that runs Swarm, so other
users fall through to HTTP, which needs a token (`token` or `SWARM_AUTH_TOKEN`).
Browser code never touches the filesystem; use `@swarm-agent/sdk/browser` there.
`resolveSwarmConnection()` returns the choice without connecting.

<copy>
node --input-type=module -e "import { SwarmClient } from '@swarm-agent/sdk'; const c = new SwarmClient(); console.log(c.config.socketPath ?? c.config.baseUrl)"
</copy>

For maintainers, the SDK builds independently with its own locked dev dependencies:

<copy>
cd packages/sdk
npm ci --ignore-scripts
npm run build
npm pack --pack-destination "$PACKAGE_OUTPUT_DIR"
</copy>

The pack contains compiled ESM, declarations, license, README and the headless
example, not source tests or node_modules. `prepack` builds locally; there are no
install/prepare hooks. TypeScript/tsx are build/test tools, not runtime dependencies.
CommonJS `require` and non-Node runtimes are not validated by this packaging step.

On the Swarm box, `http://127.0.0.1:7783` is the scoped-token listener. The AI
key the installer wrote to `~/.config/swarm/ai-key` (a new one:
`sudo bash /var/lib/swarm-headless/install.sh connect-ai`)
works only through its MCP, `http://127.0.0.1:7783/mcp`, with any MCP client;
it reaches no SDK route. For the session routes there, use a client key (a full
AI key's `swarm_create_client_key` mints one); **do not** use Desktop bootstrap
or a privileged socket. A client key reaches session operations only, not the
complete API below.
The packaged `examples/headless-session.ts` imports `@swarm-agent/sdk` and can be copied
into your consumer and run with an explicitly installed `tsx` runner. Protect its
`SWARM_SDK_TOKEN_FILE` (0600); the example asks before each tool approval and uses
explicit refresh, not timer polling. Live agent verification is a separate step.

## Quick Start

### Workers and jobs

Start with a project and its authorized folders, then give jobs to workers:
coder for code changes, designer for UI/design, finder for research, or reviewer
for independent read-only review of another job. Reviewers require `review_job`,
use detached disposable checkouts, and report without delivering. Worker models
follow account role settings unless pinned. Read each job's result before
delivery (local merge, configured pull request, or queued landing).

### Advanced: sessions, plans and orchestration

For conversational interfaces or structured multi-stage work:

1. **Interactive Chat Flow (`client.chat` / `client.sessions`)**:
   - Best for conversational assistants, interactive Q&A, and direct tool calling.
   - A standalone session has one primary workspace, can use additional authorized workspaces, and follows normal worktree/permission/plan rules. It does not carry a project's high-level Markdown context.
   - Use `client.chat.createSession()`, `client.chat.run()`, and `client.chat.stream()`.

2. **Autonomous Orchestration Flow (`client.projects` / `client.workers` / `client.deploy`)**:
   - Best for multi-agent background tasks, codebase modifications, and durable pipelines.
   - Organizes worker jobs across authorized folders with structured plans and checkpoint contracts.
   - **Project → orchestrator conversations → delegated tasks → task sessions.** Project Markdown and linked workspaces provide high-level context; each deployed task resolves its own authorized execution workspace.
   - Use `client.projects.listConversations(projectId)` and `createConversation(projectId)` for the session picker and New Conversation button. These call canonical `/v3/projects/{id}/sessions`, not generic workspace session creation. `getOrchestratorSession` reuses an existing conversation or creates one; it does not mean there can only be one.
   - Use `client.projects.createTask()` / `listTasks()` for custom task organization. Task-linked sessions must be continued through `reopenTask` with exact revision guards, not ordinary chat messages.
   - Naming a workspace session “Orchestrator” does not grant orchestration authority. Use the project conversation helper; do not supply a workspace path or spoof task metadata.
   - Models and thinking resolve from daemon account settings. Conversation creation must not overwrite provider/model preferences.

---

### 1. Initialize Client & Auto-Configure Credentials

The SDK can automatically discover credentials from your environment or Codex OAuth:

```typescript
import { SwarmClient } from '@swarm-agent/sdk';

// Owner socket when this process may use it, else http://127.0.0.1:7781
// (see "Connect to your Swarm"; pass socketPath/baseUrl/token to choose).
const client = new SwarmClient();

// 1. Ensure headless identity is bootstrapped
await client.onboarding.ensureBootstrapped({ username: 'developer', swarm_name: 'My Swarm' });

// 2. Auto-configure credentials from environment variables:
// (checks OPENAI_API_KEY, ANTHROPIC_API_KEY, GEMINI_API_KEY/GOOGLE_API_KEY, DEEPSEEK_API_KEY, etc.)
// When found, automatically applies verified provider model recommendations to the fleet & router!
const auth = await client.auth.autoConfigure();
console.log(`Provider: ${auth.primaryProvider} (Fleet configured: ${auth.fleetApplied})`);

// Or if using OpenAI Codex OAuth device flow:
if (!auth.primaryProvider) {
  await client.auth.codex.loginDevice({
    onCode: (code) => console.log(`Open ${code.verification_url} and enter ${code.user_code}`),
  });
  await client.settings.applyProviderFleet('codex');
}
```

---

### 2. Fleet & Router Model Settings

Swarm automatically optimizes models for each specific role in the agent fleet. You can customize them or apply verified recommendations in a single call:

```typescript
// Apply verified recommendations for a provider across the fleet:
// Sets Swarm Action (auto), Swarm Plan (plan), Coder, Finder, Designer, Router, and Compact.
await client.settings.applyProviderFleet('codex');

// Inspect current fleet configuration
const settings = await client.settings.agentModels();
console.log('Action Model:', settings.agent_model_settings.swarm.action.model);
console.log('Router Model:', settings.agent_model_settings.system_agents.router.model);
console.log('Coder Model:', settings.agent_model_settings.system_agents.coder.model);

// Customize individual slots:
await client.settings.setSystemAgentModel('router', {
  provider: 'codex',
  model: 'gpt-6-luna',
  thinking: 'low',
});

// Restore system defaults at any time:
await client.settings.restoreDefaults();
```

---

### 3. Explicit permission decisions

Use `describePermission(record)` to render tool details, conservative per-request `actions`,
question IDs, options and custom-response fields. Treat all text/arguments as untrusted text,
not HTML. `title` is always a plain-text heading: the request's own title, else the bash
command, the first ask-user question, or the humanized tool name and action (for example
`Manage projects: propose task (Add login)`). `parseError` leaves malformed questions deny-only. These helpers do not grant authority:
the backend enforces policy, project scope and specialized plan/proposal acceptance.

```typescript
import { describePermission, answerPermission } from '@swarm-agent/sdk';

const pending = await client.permissions.listSessionPending(sessionId);
for (const permission of pending) {
  renderPermission(describePermission(permission)); // Your UI; does not approve anything.
}

// Call from a user submit handler, with actual user-entered input:
async function submitAnswer(permission, userInput) {
  const options = answerPermission(permission, userInput);
  return await client.permissions.resolve(sessionId, permission.id, 'allow_once', options);
}
// Single question: userInput is a string (choice value or custom text).
// Structured questions: { questionId: 'actual answer', q_2: 'custom text' }.
// answerPermission serializes the backend reason/answers-map contract, not approved_arguments.answer.
// Ordinary tools: resolve(..., 'allow_once') or resolve(..., 'deny') only after a user click.
```

`resolve` returns the confirmed permission record. Transport failures and mismatched/stale
results reject; `PermissionResolutionError.result` retains a conflicting authoritative record.
A cancelled request or a prior different answer is not success. Backend sanitization can also
change returned text; review the returned record instead of blindly resubmitting. Duplicate
in-flight resolutions are rejected. `approvedArguments` supports an explicitly reviewed JSON
object where the backend supports it; it is not a question-answer channel.

Persistent actions and policy methods are advanced account-policy operations, not default UI
buttons. Project conversations require individual one-time decisions. Avoid `resolveAll` for
interactive forms: it can partially mutate on failure. Never enable bypass or change models
as incidental chat setup. Explicit auto-approval options affect ordinary tools only; ask-user
always requires actual user input.

---

### 4. Interactive Chat & Live Tool Stream

One `chat.stream()` watch per selected conversation serves every turn. It hydrates the
session, resumes `/v3/realtime/stream` from the snapshot cursor, and reconnects on its own.

```typescript
const session = await client.chat.createSession({ title: 'Repo tour', workspace_path: '/path/to/repo' });

let answer = '';
const stream = await client.chat.stream(session.id, {
  onText: (delta) => { answer += delta; render(answer); },  // deltas join to exactly the reply
  onReasoning: (_delta, full) => renderThinking(full),
  onToolStarted: (tool) => showTool(tool.callId, tool.name, tool.arguments),
  onToolDelta: (tool, chunk) => appendToolOutput(tool.callId, chunk),
  onToolCompleted: (tool) => finishTool(tool.callId, tool.status), // completed | failed | cancelled
  onPermissionRequested: (perm, resolve) => askUser(perm, resolve), // see section 3; never auto-answer
  onState: (state) => setStatus(state.status),               // syncing | live | reconnecting
  onComplete: (messages, run) => {                            // once per run, success or not
    if (run.status !== 'completed') showFailure(run.status, run.blocked_reason, messages);
    answer = '';
  },
  onError: (error) => showDisconnected(error),               // terminal: auth/protocol/retries exhausted
});
await stream.ready;                                           // hydrated and replayed: safe to send
await client.chat.sendMessage(session.id, 'Summarize the build setup.');
// Send further turns on the same watch. Switching conversations: stream.dispose(), then
// chat.stream(otherId). client.sessions.list() lists conversations.
```

Guarantees, verified end to end against the API server (see "Streaming verification" below):

- `onText` deltas concatenate to exactly the assistant text, including when the socket drops
  mid-turn: nothing is repeated and nothing is skipped; the last call carries any committed
  text beyond what streamed. Use `fullText` (2nd argument) to replace instead of append.
- A dropped socket reconnects (exponential backoff, 6 consecutive attempts per outage) and
  resumes from durable state; `state.status` is `reconnecting` meanwhile and keeps the last view.
- `onComplete(messages, run)` fires once per finished run, whatever its outcome.
  A failed provider turn has `run.status === 'failed'` and a system message in `messages`.
- Durable events (newest 1000) are in `state.snapshot.events_by_session[id]` once, in sequence order.
- Live tool objects carry `callId`, equal to `payload.call_id` on the durable `session.tool.*`
  events. When you merge history cards with live cards, match on `callId`: `tool.id` is kept
  for compatibility and can be the step-scoped instance id (`step-2:call_…`).
- A conversation whose history no longer fits the hydrate window (for example after a very
  large tool result) still opens: the watch retries with `history.manifest_policy: 'manifest'`,
  keeps the newest window, and sets `state.historyOmitted = true` with
  `state.historyOmissions` (resource and `next_cursor`) so you can show "older history not
  loaded" instead of presenting the window as complete.

`state.messages` (a 200-message tail) and `state.live` are the rendering authority;
callbacks are conveniences. Clearing context: `client.sessions.clearContext(id, { expectedLastEventSeq })`
starts a fresh model context in the same conversation, but the daemon allows it only for an
idle project conversation (`SwarmConflictError` otherwise, nothing changes); for a standalone
chat, create a new session.

**Streaming verification.** `swarmd/internal/api` `TestSDKChatStreamE2E` (opt-in:
`npm run build`, then `SWARM_SDK_E2E=1 go test ./internal/api -run TestSDKChatStreamE2E`)
drives the built package (`e2e/chat-stream.e2e.mjs`) against the real API server, store,
executor and realtime socket with a scripted provider: incremental text, a socket drop mid-turn
while durable checkpoints lag, completion, failure, session switching and context clear. It does
not exercise a real model, tool calls or permission prompts, or token authentication.

`chat.run()` (polling, returns the final reply) remains available for scripts; keep a
`chat.stream()` watch open when tool permissions need an explicit answer.

---

### 5. Native WebSocket Streaming Bridge & Browser Client

Swarmagent communicates in real-time over bidirectional WebSockets (`/v3/realtime/stream`). The SDK provides an out-of-the-box WebSocket Bridge for Node.js backends and a universal browser client for web frontends (React, Vue, Svelte, or Vanilla JS):

#### Backend (Node.js): Mount WebSocket Bridge
```typescript
import http from 'node:http';
import { SwarmClient } from '@swarm-agent/sdk';

const server = http.createServer((req, res) => { /* app routes */ });
const client = new SwarmClient();

// Mounts bidirectional WebSocket bridge on /ws:
// Hooks directly into Swarm daemon's realtime WebSocket outbox, multiplexing tokens, reasoning, and tool calls.
client.chat.attachWebSocket(server, {
  path: '/ws',
  autoApprovePermissions: false, // Explicit server policy; browser cannot enable it
});

server.listen(3456, '127.0.0.1');
```

#### Frontend (Browser): Real-Time WebSocket Hooking
```typescript
import { SwarmBrowserChat, describePermission, answerPermission } from '@swarm-agent/sdk/browser';

const chat = new SwarmBrowserChat(); // same-origin ws/wss selected automatically
chat.on('state', state => {
  // Replace durable history and transient streams separately; never append fullText to history.
  renderConversation(state.messages, state.live, state.snapshot);
  // snapshot.current_run_state_by_session[state.sessionId] owns busy/waiting/failed status.
});
chat.on('error', showError);
chat.on('disconnected', disableComposer);

// 1. Hook into live tool execution and token streams
chat.on('text', (delta, fullText) => renderLiveMarkdown(fullText));
chat.on('reasoning', (delta, fullText) => renderModelThinking(fullText));
chat.on('tool_start', (tool) => renderToolStartedCard(tool.callId, tool.name, tool.arguments));
chat.on('tool_delta', (tool, delta) => appendToolOutput(tool.callId, delta));
chat.on('tool_done', (tool) => markToolCompleted(tool.callId, tool.durationMs));
// Replace pending cards on hydration/live updates/reconnect, keyed by permission.id.
// Preserve unsent input for unchanged requests; do not submit during render.
chat.on('permissions', pending => renderPendingForms(pending.map(describePermission)));
chat.on('permission_error', (id, error) => showPermissionError(id, error));
chat.on('permission_removed', id => removePermissionForm(id));

// Wire to your form's explicit user-submit handler. Never remove a card on send alone.
async function onPermissionSubmit(permission, action, userInput) {
  try {
    const result = await chat.resolvePermission(permission.id, action,
      action === 'allow_once' && describePermission(permission).kind === 'ask-user'
        ? answerPermission(permission, userInput) : {});
    showConfirmedDecision(result.permission);
  } catch (error) { showPermissionError(permission.id, error); }
}

// 2. Connect & subscribe to session
await chat.connect();
await chat.subscribe('session_123');

// 3. Send messages directly over WebSocket
const requestId = chat.sendMessage('Inspect the git repository and list modified files.');
// `accepted` acknowledges durable dispatch. Never blindly resend an uncertain message.
// Retain requestId if your UI offers an explicit retry, and pass it as argument 4.
// Switching: await chat.subscribe(otherSession.id). Reconnect rehydrates selected history.
// Unmount: chat.dispose(). Sends that cannot go out throw SwarmChatNotReadyError (nothing was
// sent): its message is fit to show people, and `code` is chat_reconnecting (retry shortly),
// chat_disconnected (call connect()) or chat_not_subscribed (subscribe first).
```

Backend conversation routes should use the same project identity for listing and creation:
```typescript
const project = await client.projects.ensureProject('Social content');
const conversations = await client.projects.listConversations(project.id, { limit: 100 });
const created = await client.projects.createConversation(project.id, {
  title: 'New campaign', clientRequestId: crypto.randomUUID(),
});
// Return created.id to the browser; await chat.subscribe(created.id) before sending.
```

Conversation titles come from Swarm: a conversation created without a `title` is named from
its first message (refined later when a title model is available), and `listConversations`
returns it. Rename with `client.sessions.rename(id, title)` (or
`client.projects.renameConversation(id, title)`); a renamed title is kept.

Assistant text can contain `<copy label="…">…</copy>` snippet tags, which Swarm's own apps
render as copy buttons. `chat.run`, `chat.listMessages` and `chat.stream` (and the browser
bridge built on it) turn them into Markdown code blocks by default; pass `rawCopyTags: true`
to keep them. For text read elsewhere (`sessions.get`, `realtime.watchSession`), use the
exported `copyTagsToMarkdown(text)`.

Use `state` as the conversation rendering authority, including initial history, failed/empty
turns and durable reconnect recovery. `text`/tool callbacks are convenience notifications.
History is a bounded 200-message tail; use canonical sync hydration for additional history.
The browser bridge requires an explicit selected session and never guesses a recent account
session. Keep it on host loopback; network/multi-user apps must supply `authorize` and their
own per-session authorization. Foreign browser origins are rejected. Auto approval never
chooses an `ask-user` answer: render the question and send the operator's reply explicitly.

`resolvePermission` now returns a promise: await it and handle rejection. Browser and bridge
must be upgraded together; resolution frames require `requestId` and acknowledgements carry
`result`. `permission_removed` means no longer pending, not approval. `permissions` is a
rendering projection of the canonical session view, not a second durable authority. Timeout,
disconnect, disposal or session switch rejects uncertain submissions; reconnect rehydrates
without replaying decisions. To explicitly rehydrate after an acknowledgement timeout, retain
`chat.sessionId`, call `chat.unsubscribe()`, then `await chat.subscribe(retainedId)`; never resend
the decision automatically. Review refreshed state before manually retrying. No UI can infer
exactly-once application from a lost acknowledgement. `onPermissionError` is recoverable and
does not close the watch; `onError` reports stream failures. Existing string-reason resolver
calls remain supported, while options objects carry structured replies and reviewed arguments.

Runnable terminal examples: `examples/chat-quickstart.ts`, `examples/orchestrator-quickstart.ts`
and their shared `examples/interactive-permissions.ts`. They require a preconfigured daemon,
ask for actual terminal input, and never configure credentials, model fleets or bypass.

---

### 5. Local Desktop Bootstrap & Scoped Tokens

```typescript
// Bootstrap local session credentials from Desktop
const session = await client.auth.bootstrapDesktopSession();
console.log(`Authenticated as user ${session.user_id}`);

// Create a scoped deploy token for CI/CD or webhooks
const { token, record } = await client.auth.createScopedToken({
  name: 'GitHub Actions Trigger',
  scopes: ['automations:trigger'],
  expires_in_seconds: 86400, // required: every key expires (at most 365 days)
});
console.log(`Created deploy token: ${token} (hint: ${record.token_hint})`);

// List active tokens
const tokens = await client.auth.listScopedTokens();
```

### 3. Trigger Workers On-Demand with Runtime Context

```typescript
// Trigger an automation with webhook/CI context
const result = await client.automations.trigger({
  worker_id: 'ci_test_analyzer',
  context: {
    event: 'push',
    commit_sha: '44a93badc',
    branch: 'dev',
    failed_tests: ['TestWorkerRetry'],
  },
});

console.log(`Trigger admitted! Occurrence ID: ${result.occurrence.id}`);
console.log(`Worker execution session: ${result.occurrence.session_id}`);
```

### 4. Create and Manage Sessions

```typescript
// Create a new autonomous coding session
const newSession = await client.sessions.create({
  title: 'Investigate Test Flakes',
  workspace_path: '/path/to/project',
  agent: 'coder',
});

// Send a message
await client.sessions.sendMessage(newSession.id, {
  content: 'Please analyze why TestWorkerRetry is timing out.',
});

// Poll until the agent run finishes
const finishedSession = await client.sessions.waitForRun(newSession.id, {
  timeoutMs: 120_000,
});
```

### 5. Workspaces and Health

```typescript
// Check daemon health
const health = await client.system.health();
console.log('Daemon healthy:', health.ok);

// List registered workspaces
const workspaces = await client.workspaces.list();
for (const ws of workspaces) {
  console.log(`- ${ws.name} (${ws.path})`);
}
```

### 6. Projects, Autonomous Tasks, and Orchestrate Primitives

Manage multi-workspace projects, autonomous tasks, route previews, plan review lifecycles, and opaque event sync streams:

A project groups registered workspaces: the existing folders or repositories the
work lives in (`client.workspaces.add({ path, context_only: true,
require_existing_content: true, make_current: false })`), not an empty
repository created for it. Creating it makes Swarm write the project's
`project.md` (the orchestrator's context) from the workspaces' AGENTS.md, README
and file tree; `context_generation.status` moves from `pending`/`running` to
`ready` or `failed` (`retryContext(id, attempt)`). Open orchestrator
conversations once it is `ready`. Workspace and `project_context` edits through
`update` are refused until then.

```typescript
// Create a new project aggregating the person's existing workspaces
let project = await client.projects.create({
  name: 'Mobile Core Redesign',
  description: 'Refactor mobile navigation and auth screens',
  workspaces: [{ path: '/workspaces/mobile-app', role: 'primary_code' }],
});
// Re-read until project.md is generated (or follow realtime project updates)
project = await client.projects.get(project.id);
if (project.context_generation?.status === 'failed') {
  project = await client.projects.retryContext(project.id, project.context_generation.attempt);
}

// Create an autonomous task with agent routing
const { task, model_preview } = await client.projects.createTask(project.id, {
  title: 'Implement OAuth refresh token rotation',
  agent: 'coder',
  feature_size: 'small',
  prompt: 'Add proactive token refresh before expiration with retry backoff',
});

// Preview how Orchestrate routes and models a proposed task before creation
const preview = await client.projects.previewTask(project.id, {
  title: 'Audit API authorization checks',
  agent: 'finder',
});

// Guarded plan acceptance: enforce exact session, plan, and definition revision
const approved = await client.projects.approveTask(project.id, task.id, {
  session_id: task.session_id,
  plan_id: task.plan_binding?.plan_id,
  definition_revision: task.plan_binding?.definition_revision,
});

// Reopen a task with focused human feedback directives
await client.projects.reopenTask(project.id, task.id, {
  feedback: 'Unit test flake on Node 20; please address.',
});

// Refine a plan based on feedback or error recovery
await client.projects.refineTask(project.id, task.id, {
  feedback: 'Split implementation into schema migration then service logic',
  agent: 'coder',
});

// Stream or replay outbox events starting after an opaque cursor
const streamResult = await client.projects.replayEvents('opaque_cursor_token');

// Integrate a finished task's commits into its captured target branch
const integration = await client.projects.integrateTask(project.id, task.id);
```

`integrateTask` reads the task first (`getTask`, which observes Git) and sends the exact
selection the daemon checks: `session_id` (the task's current session) and the captured
`source_branch`/`target_branch` (from `task.delivery_assessment`, else the task's
`worktree_branch`/`base_branch`, else its active attempt), plus `revision`, `attempt_id` and the
observed `source_head`/`target_head`. Override any field with
`integrateTask(projectId, taskId, { selection: { target_branch: 'main' } })`; supplying
`session_id`, `source_branch` and `target_branch` skips the read. A task with no captured
worktree lane throws `SwarmValidationError` without calling the daemon; a stale selection, dirty
checkout or non-actionable delivery state comes back as `SwarmConflictError` and nothing is
integrated.

### 7. Programmable Push Alert Webhooks

Register push alert webhook destinations to receive HMAC-signed real-time notifications on worker lifecycle events (`started`, `succeeded`, `failed`, `retry_exhausted`):

```typescript
// Register a signed webhook destination
const webhook = await client.automations.createWebhook({
  url: 'https://api.mycompany.com/webhooks/swarm',
  secret: 'whk_hmac_secret_key',
  format: 'generic', // 'generic' | 'slack' | 'discord' | 'telegram'
  events: ['failed', 'retry_exhausted'], // or ['*'] for all events
});

// Test the webhook with an immediate ping
const testResult = await client.automations.testWebhook({
  id: webhook.id,
  worker_title: 'Database Backup',
});
console.log(`Ping delivered: HTTP ${testResult.result?.status_code} in ${testResult.result?.duration_ms}ms`);

// List configured webhooks
const webhooks = await client.automations.listWebhooks();

// Delete a webhook
await client.automations.deleteWebhook(webhook.id);
```

### 8. Deliverables Hub & Agent Mailbox

Submit completed work (social media drafts, reports, test alerts) from background workers to the local Swarm Agent Mailbox for human review, editing, and one-click publication actions:

```typescript
// Submit finished worker output to the Mailbox
const deliverable = await client.deliverables.submit({
  title: 'Weekly Community Launch Thread',
  kind: 'social_post',
  worker_id: 'social_media_bot',
  payload: {
    posts: [
      { text: '1/3 Announcing our new distributed agent architecture...' },
      { text: '2/3 Run workers in the cloud, review in your local mailbox.' },
      { text: '3/3 Try it today with @swarm-agent/sdk.' }
    ]
  },
  action_contract: {
    action: 'publish_x_post',
    target_secret_ref: 'gcp:x-api-key'
  }
});

// List unreviewed deliverables in the inbox
const pending = await client.deliverables.list({ status: 'pending_review' });
console.log(`Pending inbox items: ${pending.length}`);

// Approve deliverable and trigger publication action
const result = await client.deliverables.approve(deliverable.id);
console.log('Published:', result.action_result);
```

### 9. Withdrawn cloud recipes — migration required

**The historical recipes below are unsupported and all built-in generation/execution calls now reject them.** They are retained here only as migration context, not runnable instructions or verified measurements. The old latency, capacity, cost and image-size claims were not qualified for the canonical daemon and must not be relied upon.

Use `generateOptimizedDockerfile({ runtimeImage })` with the exact
`ghcr.io/swarm-agent/swarm-headless@sha256:<digest>` from the qualified release manifest.
No digest is invented or selected automatically. See
[the supported application recipe](../../containers/headless/app/README.md).
Cloud Run, Cloud Run worker actors, and the old Compute Engine source installer
are withdrawn: they do not preserve canonical runtime, private listener, and
single-writer durable state contracts. Legacy manifest parsing remains available
for migration, not as a supported deployment preset.

<details>
<summary>Historical unsupported API examples (do not execute)</summary>

Deploy Swarm as a self-contained, serverless or dedicated cloud service on Google Cloud Platform with sub-1.5s cold starts and true scale-to-zero ($0 idle cost).

The deployment module supports both programmatic definitions and file-driven workflows (`swarm.deploy.json`), producing optimized multi-stage distroless containers and execution pipelines directly from the canonical repository ([https://github.com/swarm-agent/swarm](https://github.com/swarm-agent/swarm)).

#### The Easy 1-2-3 Deployment Flow for AI Agents & Developers

##### Step 1: Create or Load a Deployment Manifest

Use pre-tuned presets (`latency-optimized`, `zero-idle-cost`, `free-tier-vm`, `always-on`) or define a custom `swarm.deploy.json`:

```typescript
import { createSwarmClient } from '@swarm-agent/sdk';

const client = createSwarmClient();

// Programmatic creation with latency-optimized preset
const manifest = client.deploy.createManifest({
  preset: 'latency-optimized',
  name: 'swarm-cloud-agent',
  gcpCloudRun: {
    serviceName: 'swarm-agent-service',
    region: 'us-central1',
    memory: '512Mi',
    cpu: '1',
    minInstances: 0, // True scale-to-zero ($0 idle spend)
    cpuBoost: true,  // Startup CPU Boost for <1.5s cold boot
  },
  env: {
    SWARM_ENV: 'production',
  },
});
```

Or load from a `swarm.deploy.json` file in your workspace:

```json
{
  "version": "1.0",
  "name": "swarm-gcp-service",
  "target": "gcp-cloud-run",
  "source": {
    "type": "github",
    "repository": "https://github.com/swarm-agent/swarm",
    "branch": "main",
    "buildStrategy": "distroless"
  },
  "gcpCloudRun": {
    "serviceName": "swarmd",
    "region": "us-central1",
    "cpu": "1",
    "memory": "512Mi",
    "minInstances": 0,
    "maxInstances": 10,
    "concurrency": 80,
    "port": 18080,
    "cpuBoost": true,
    "executionEnvironment": "gen2"
  }
}
```

##### Step 2: Generate Deployment Plan & Artifacts

Generate the complete deployment plan, including standalone multi-stage Dockerfiles, Cloud Build configurations, and deployment scripts:

```typescript
// Generate the full structured deployment plan
const plan = client.deploy.plan(manifest);
console.log('Target Architecture:', plan.target);
console.log('Cost Summary:', plan.summary);
console.log('Recommendations:', plan.recommendations);

// Or generate files ready to be written to disk
const files = client.deploy.generateArtifacts(manifest);
// files['Dockerfile']       -> Stripped multi-stage distroless container (<35MB)
// files['.dockerignore']    -> Excludes git, node_modules, logs, and secrets
// files['cloudbuild.yaml']  -> Google Cloud Build pipeline
// files['deploy.sh']        -> Executable bash deployment script
```

##### Step 3: Execute Deployment (Programmatic or Command-Driven)

You can either execute the deployment pipeline programmatically in a single SDK call, write artifacts to disk, or inspect the shell commands:

```typescript
// Option A: Programmatic Execution (0-1 in 1 line of code)
const result = await client.deploy.execute(manifest, {
  // dryRun: true, // Optional: simulate pipeline without mutating cloud resources
  onLog: (line, phase) => console.log(`[${phase}] ${line}`),
});

console.log('Deploy Status:', result.success ? 'SUCCESS' : 'FAILED');
console.log('Elapsed Time:', `${result.durationMs}ms`);
console.log('Service URL:', result.serviceUrl || result.externalIp);

// Option B: Write artifacts to directory
await client.deploy.writeArtifacts('./deploy-output', manifest);

// Option C: Inspect sequential shell commands
const commands = client.deploy.generateCommands(manifest);
for (const step of commands) {
  console.log(`[${step.phase.toUpperCase()}] ${step.title}`);
  console.log(`Command: ${step.command}`);
  console.log(`Why:     ${step.explanation}\n`);
}
```

The generated deployment commands execute:
1. **Cloud Build**: Compiles a static Go binary with stripped symbol tables (`-ldflags="-s -w"`) inside a distroless runtime (`gcr.io/distroless/static-debian12:nonroot`).
2. **Cloud Run Deploy**: Deploys the service with `--cpu-boost` (Startup CPU Boost) and `--execution-environment gen2`.
3. **Health Verification**: Queries the service URL and verifies `/v1/system/health`.

The former benchmark table has been withdrawn: there is no revision-bound live
qualification evidence for those image-size, latency, capacity or cost claims.

#### Free Tier Compute Engine (GCE) & Dynamic Context Management

For persistent background daemons on Google Cloud's Free Tier ($0/month) with dynamic context management:

```typescript
const gceManifest = client.deploy.createManifest({
  preset: 'free-tier-vm',
  name: 'swarm-free-tier',
  context: {
    instructions: 'You are an autonomous background support agent.',
    prompt: 'Monitor incoming notifications and update status.',
    model: 'gemini-3.8-flash',
    thinking: 'low',
    workspaceUrl: 'https://github.com/my-org/my-project.git',
  },
  env: {
    APP_ENV: 'production',
  },
  gcpCompute: {
    instanceName: 'swarm-free-vm',
    machineType: 'e2-micro', // 2 vCPU, 1GB RAM (Free Tier eligible)
    diskSizeGb: 30,          // 30GB Standard persistent disk (Free Tier eligible)
  },
});

const gcePlan = client.deploy.plan(gceManifest);
// Generates:
// 1. startup-script.sh with automated swapfile creation, systemd unit, and /usr/local/bin/swarm-sync-context.
// 2. deploy-vm.sh provisioning the GCE VM unattended.
// 3. AGENTS.md & PROMPT.txt context files.

// Execute headless deployment
const gceResult = await client.deploy.execute(gcePlan);
```

##### Managing Context Dynamically via Instance Metadata (No Rebuilds)
Once the VM is running, users and orchestrators can dynamically update the agent's instructions, prompts, and environment without recreating the VM:
```bash
# Update instructions or prompt on a live VM
gcloud compute instances add-metadata swarm-free-vm \
  --metadata swarm-prompt="Audit and fix outstanding PR comments" \
  --zone us-central1-a

# The VM's /usr/local/bin/swarm-sync-context automatically applies the new context and reloads swarmd!
```

#### Multi-Cloud Extensibility

The `@swarm-agent/sdk` deployment architecture is designed for multi-cloud extensibility. Custom providers (for AWS ECS, Azure Container Apps, Fly.io, or Kubernetes) can be registered at runtime:

```typescript
import type { CloudDeployProvider } from '@swarm-agent/sdk';

class CustomCloudProvider implements CloudDeployProvider {
  readonly target = 'my-cloud';
  validate(manifest) { return { valid: true, errors: [], warnings: [] }; }
  plan(manifest) { /* ... */ }
  generateArtifacts(manifest) { return { 'deploy.yaml': '...' }; }
  generateCommands(manifest) { return [/* ... */]; }
}

client.deploy.registerProvider(new CustomCloudProvider());
```

</details>

## Error Handling

All failed API responses throw typed `SwarmApiError` instances:

```typescript
import { SwarmForbiddenError, SwarmNotFoundError, SwarmTimeoutError } from '@swarm-agent/sdk';

try {
  await client.sessions.get('non_existent_session');
} catch (err) {
  if (err instanceof SwarmNotFoundError) {
    console.error('Session not found (404)');
  } else if (err instanceof SwarmForbiddenError) {
    console.error('Missing required scope (403):', err.message);
  } else if (err instanceof SwarmTimeoutError) {
    console.error('Request timed out after', err.timeoutMs, 'ms');
  }
}
```

## License

Apache-2.0

### Application-owned agents and conversations

`client.apps` provides durable named instructions/context, revision-pinned V3 conversations,
owned reopen/result reads and idempotent event messages. See [Application agents](APPLICATIONS.md)
for examples, revision semantics and the private local-API transport requirement. Existing
`projects` and `workers` remain the task and background-execution authorities.

## Custom headless UI

See [HEADLESS_UI.md](./HEADLESS_UI.md) for private in-container administrative setup, typed provider/settings discovery, API keys and Codex device/manual/browser sign-in, workspace creation, and `client.realtime.watchSession()` with canonical V3 replay and cancellation. The container's session-only listener does not expose administrative APIs.
