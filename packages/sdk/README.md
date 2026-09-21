# @swarm/sdk

Official TypeScript SDK for [Swarm](https://github.com/swarm-ai/swarm), the local-first AI coding workspace.

`@swarm/sdk` provides a typed, zero-dependency client to interact programmatically with the Swarm daemon (`swarmd`) over HTTP and local Unix domain sockets.

## Features

- **Zero Runtime Dependencies**: Works out-of-the-box in Node.js, Bun, Deno, and modern browser/edge environments.
- **Zero-Conf Unix Sockets**: Native support for daemon Unix domain sockets (`/run/swarmd/swarmd.sock`) for automatic peer identity.
- **Granular Scoped Tokens**: Create, list, revoke, and purge scoped deploy tokens (`swk_...`) with fine-grained permissions (`automations:trigger`, `sessions:read`, `sessions:write`, `admin`).
- **Worker / Automation V2 Triggers**: Trigger workers on-demand with dynamic caller context (e.g. GitHub Webhooks, CI/CD events, alert payloads).
- **Session & Workspace Lifecycle**: Create durable V3 sessions, manage runs, send messages, poll for completion, and inspect workspaces.
- **Typed Error Hierarchy**: `SwarmAuthError` (401), `SwarmForbiddenError` (403), `SwarmNotFoundError` (404), `SwarmConflictError` (409), `SwarmTimeoutError`.

## Installation

```bash
npm install @swarm/sdk
# or
pnpm add @swarm/sdk
```

## Quick Start

### 1. Initialize Client

```typescript
import { createSwarmClient } from '@swarm/sdk';

// Connect to local Swarm daemon (defaults to http://127.0.0.1:18080 or SWARM_API_URL)
const client = createSwarmClient({
  baseUrl: process.env.SWARM_API_URL || 'http://127.0.0.1:18080',
  token: process.env.SWARM_DEPLOY_TOKEN, // Optional: scoped deploy token
});
```

### 2. Local Desktop Bootstrap & Scoped Tokens

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
// Trigger a worker with webhook/CI context
const result = await client.workers.trigger({
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

### 6. Programmable Push Alert Webhooks

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

### 7. Deliverables Hub & Agent Mailbox

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
      { text: '3/3 Try it today with @swarm/sdk.' }
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

## Error Handling

All failed API responses throw typed `SwarmApiError` instances:

```typescript
import { SwarmForbiddenError, SwarmNotFoundError, SwarmTimeoutError } from '@swarm/sdk';

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
