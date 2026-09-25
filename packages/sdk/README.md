# @swarm/sdk

Official TypeScript SDK for [Swarm](https://github.com/swarm-agent/swarm), the local-first AI coding workspace.

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

### 8. Cloud Deployment SDK (GCP Cloud Run & Compute Engine)

Deploy Swarm as a self-contained, serverless or dedicated cloud service on Google Cloud Platform with sub-1.5s cold starts and true scale-to-zero ($0 idle cost).

The deployment module supports both programmatic definitions and file-driven workflows (`swarm.deploy.json`), producing optimized multi-stage distroless containers and execution pipelines directly from the canonical repository ([https://github.com/swarm-agent/swarm](https://github.com/swarm-agent/swarm)).

#### The Easy 1-2-3 Deployment Flow for AI Agents & Developers

##### Step 1: Create or Load a Deployment Manifest

Use pre-tuned presets (`latency-optimized`, `zero-idle-cost`, `free-tier-vm`, `always-on`) or define a custom `swarm.deploy.json`:

```typescript
import { createSwarmClient } from '@swarm/sdk';

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

#### Optimal GCP Deployment Settings Benchmark Reference

| Setting | Recommended Value | Impact on Latency / Cost |
| :--- | :--- | :--- |
| **Container Runtime** | `distroless` (`static-debian12`) | Image size <35MB; pulls in <800ms vs 3.5s for alpine/full OS. |
| **Compiler Flags** | `CGO_ENABLED=0 -ldflags="-s -w"` | Strips debug tables and symbols; reduces binary from 45MB to ~25MB. |
| **UPX Compression** | **Omitted / Disabled** | UPX decompression increases startup CPU overhead, adding ~1s to cold starts. |
| **Startup CPU Boost** | `--cpu-boost` | Temporarily doubles CPU during container launch; cuts cold start to **<1.5s**. |
| **Execution Environment** | `--execution-environment gen2` | Modern Linux kernel and lower gVisor overhead. |
| **Min Instances** | `--min-instances 0` | **$0.00/month idle cost**; only pay for active processing. |
| **Memory Allocation** | `--memory 512Mi` | Optimal balance for Go GC and memory footprint. |
| **Concurrency** | `--concurrency 80` | Allows multiple concurrent agent interactions per container instance. |

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

The `@swarm/sdk` deployment architecture is designed for multi-cloud extensibility. Custom providers (for AWS ECS, Azure Container Apps, Fly.io, or Kubernetes) can be registered at runtime:

```typescript
import type { CloudDeployProvider } from '@swarm/sdk';

class CustomCloudProvider implements CloudDeployProvider {
  readonly target = 'my-cloud';
  validate(manifest) { return { valid: true, errors: [], warnings: [] }; }
  plan(manifest) { /* ... */ }
  generateArtifacts(manifest) { return { 'deploy.yaml': '...' }; }
  generateCommands(manifest) { return [/* ... */]; }
}

client.deploy.registerProvider(new CustomCloudProvider());
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
