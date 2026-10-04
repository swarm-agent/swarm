# @swarm-agent/sdk

Official TypeScript SDK for [Swarm](https://github.com/swarm-agent/swarm), the local-first AI coding workspace.

`@swarm-agent/sdk` provides a typed, zero-dependency client to interact programmatically with the Swarm daemon (`swarmd`) over HTTP and local Unix domain sockets.

## Features

- **Zero Runtime Dependencies**: Works out-of-the-box in Node.js, Bun, Deno, and modern browser/edge environments.
- **Zero-Conf Unix Sockets**: Native support for daemon Unix domain sockets (`/run/swarmd/swarmd.sock`) for automatic peer identity.
- **Granular Scoped Tokens**: Create, list, revoke, and purge scoped deploy tokens (`swk_...`) with fine-grained permissions (`automations:trigger`, `sessions:read`, `sessions:write`, `admin`).
- **Worker / Automation V2 Triggers**: Trigger workers on-demand with dynamic caller context (e.g. GitHub Webhooks, CI/CD events, alert payloads).
- **Session & Workspace Lifecycle**: Create durable V3 sessions, manage runs, send messages, poll for completion, and inspect workspaces.
- **Typed Error Hierarchy**: `SwarmAuthError` (401), `SwarmForbiddenError` (403), `SwarmNotFoundError` (404), `SwarmConflictError` (409), `SwarmTimeoutError`.

## Unpublished npm candidate

Node 22+ and ESM imports are the validated packaging target. The prerelease name
is not a claim of registry ownership or publication. Install the local candidate
from a clean consumer directory (no Swarm checkout or Desktop required):

<copy>
npm install "$SDK_TARBALL"
node --input-type=module -e "import { SwarmClient } from '@swarm-agent/sdk'; console.log(typeof SwarmClient)"
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

For the headless container, use the matching `@swarm-agent/cli` candidate and explicitly
export a scoped token through `swarm-headless setup -- sdk-token`. Connect to
`http://127.0.0.1:7783`; **do not** use Desktop bootstrap or a privileged socket.
The headless listener allows session operations only, not the complete API below.
The packaged `examples/headless-session.ts` imports `@swarm-agent/sdk` and can be copied
into your consumer and run with an explicitly installed `tsx` runner. Protect its
`SWARM_SDK_TOKEN_FILE` (0600); the example asks before each tool approval and uses
explicit refresh, not timer polling. Live agent verification is a separate step.

## Quick Start

### Chat Flow vs. Autonomous Orchestration Flow

Swarm supports two distinct modes of execution depending on your application goals:

1. **Interactive Chat Flow (`client.chat` / `client.sessions`)**:
   - Best for conversational assistants, interactive Q&A, and direct tool calling.
   - A standalone session has one primary workspace, can use additional authorized workspaces, and follows normal worktree/permission/plan rules. It does not carry a project's high-level Markdown context.
   - Use `client.chat.createSession()`, `client.chat.run()`, and `client.chat.stream()`.

2. **Autonomous Orchestration Flow (`client.projects` / `client.workers` / `client.deploy`)**:
   - Best for multi-agent background tasks, codebase modifications, and durable pipelines.
   - Deploys specialized subagent fleets (Coder, Finder, Designer, Router, Compact) across bounded repository worktrees with structured checkpoint contracts.
   - **Project → orchestrator conversations → delegated tasks → task sessions.** Project Markdown and linked workspaces provide high-level context; each deployed task resolves its own authorized execution workspace.
   - Use `client.projects.listConversations(projectId)` and `createConversation(projectId)` for the session picker and New Conversation button. These call canonical `/v3/projects/{id}/sessions`, not generic workspace session creation. `getOrchestratorSession` reuses an existing conversation or creates one; it does not mean there can only be one.
   - Use `client.projects.createTask()` / `listTasks()` for custom task organization. Task-linked sessions must be continued through `reopenTask` with exact revision guards, not ordinary chat messages.
   - Naming a workspace session “Orchestrator” does not grant orchestration authority. Use the project conversation helper; do not supply a workspace path or spoof task metadata.
   - Models and thinking resolve from daemon account settings. Conversation creation must not overwrite provider/model preferences.

---

### 1. Initialize Client

Connect to an already configured, authenticated daemon. Creating a chat UI must not
change provider credentials, model assignments or permission policy.

```typescript
import { SwarmClient } from '@swarm-agent/sdk';

const client = new SwarmClient({
  baseUrl: process.env.SWARM_API_URL,
  socketPath: process.env.SWARM_SOCKET_PATH,
});
```

---

### 2. Explicit permission decisions

Use `describePermission(record)` to render tool details, conservative per-request `actions`,
question IDs, options and custom-response fields. Treat all text/arguments as untrusted text,
not HTML. `parseError` leaves malformed questions deny-only. These helpers do not grant authority:
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

### 3. Interactive Chat & Live Tool Stream

Build responsive chat UIs that stream model text, reasoning, and live tool execution cards like Swarm Desktop:

```typescript
// 1. Create a chat session
const session = await client.chat.createSession({
  title: 'Repository Exploration',
  workspace_path: '/path/to/project',
});

// 2. Stream live text, reasoning, and tool calls
const stream = await client.chat.stream(session.id, {
  onText: (delta, fullText) => process.stdout.write(delta),
  onReasoning: (delta) => console.log('[Thinking]', delta),
  onToolStarted: (tool) => console.log(`[Tool Started] ${tool.name}`),
  onToolDelta: (tool, delta) => console.log(`[Tool Output] ${delta}`),
  onToolCompleted: (tool) => console.log(`[Tool Done] ${tool.name} in ${tool.durationMs}ms`),
  onPermissionRequested: async (perm, resolve) => {
    // Your UI must wait for a real user gesture. Keep errors visible and retain the form.
    renderPermissionForm(describePermission(perm), async (action, userInput) => {
      try {
        const result = await resolve(action, action === 'allow_once' && describePermission(perm).kind === 'ask-user'
          ? answerPermission(perm, userInput) : {});
        showConfirmedDecision(result.permission);
      } catch (error) { showPermissionError(perm.id, error); }
    });
  },
  onPermissionRemoved: id => removePermissionForm(id), // No longer pending; not necessarily approved.
  onPermissionError: (error, perm) => showPermissionError(perm.id, error),
  onState: (state) => renderConversation(state.messages, state.live, state.snapshot),
  onComplete: () => console.log('Turn finished; stream remains open'),
});

await stream.ready; // Hydration and replay MUST finish before dispatch.
await client.chat.sendMessage(session.id, 'Find all configuration files and summarize them.');
// Keep this watch for subsequent messages. Dispose only on switch/unmount/shutdown.
// stream.done means the watch ended, not that a single assistant turn finished.

// 3. Or use chat.run while the watch above serves the explicit permission UI
const response = await client.chat.run(session.id, {
  message: 'What is the git status of this repository?',
  timeoutMs: 120_000,
});
console.log(response.reply);
```

---

### 4. Native WebSocket Streaming Bridge & Browser Client

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
chat.on('tool_start', (tool) => renderToolStartedCard(tool.id, tool.name, tool.arguments));
chat.on('tool_delta', (tool, delta) => appendToolOutput(tool.id, delta));
chat.on('tool_done', (tool) => markToolCompleted(tool.id, tool.durationMs));
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
// Unmount: chat.dispose(). Disconnected sends throw instead of silently disappearing.
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
  expires_in_seconds: 86400, // 24 hours
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

```typescript
// Create a new project aggregating workspaces
const project = await client.projects.create({
  name: 'Mobile Core Redesign',
  description: 'Refactor mobile navigation and auth screens',
  workspaces: [{ path: '/workspaces/mobile-app', role: 'primary_code' }],
});

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
```

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
