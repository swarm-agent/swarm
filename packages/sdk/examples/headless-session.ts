import { open } from 'node:fs/promises';
import { constants } from 'node:fs';
import { createInterface } from 'node:readline/promises';
import { pathToFileURL } from 'node:url';
import { SwarmClient } from '../src/index.js';

// This source-tree example uses the existing SDK; npm packaging is separate.
// No Desktop bootstrap, privileged socket, automatic approval, or status polling.
export async function runHeadlessSession(
  client: SwarmClient,
  prompt: string,
  ask: (question: string) => Promise<string>,
  show: (value: string) => void,
): Promise<void> {
  const session = await client.sessions.create({
    title: 'Headless SDK session', workspace_path: '/project', mode: 'auto',
  });
  show(`Session: ${session.id}`);
  const receipt = await client.sessions.sendMessage(session.id, { content: prompt });
  if (!receipt?.run_intent?.run_id) throw new Error('Prompt accepted without a run receipt; inspect the session before retrying');
  show(`Run: ${receipt.run_intent.run_id}; receipt is not completion.`);
  for (;;) {
    const detail = await client.sessions.get(session.id);
    const raw = detail.raw as Record<string, any>;
    show(JSON.stringify({
      state: detail.state, messages: detail.messages,
      active_run_intent: raw.active_run_intent,
      current_run_state: raw.current_run_state,
      pending_permissions: raw.pending_permissions,
      active_plan: raw.active_plan,
    }, null, 2));
    const command = (await ask('refresh | allow <permission-id> | deny <permission-id> | stop | quit: ')).trim();
    if (command === 'quit') return; // Disconnecting does NOT stop a durable run.
    if (command === 'refresh') continue; // Explicit user refresh, never a timer.
    if (command === 'stop') {
      const runId = raw.active_run_intent?.run_id;
      if (!runId) { show('No active run in the current snapshot.'); continue; }
      await client.sessions.stopRun(session.id, { run_id: runId });
      continue;
    }
    const match = /^(allow|deny) (\S+)$/.exec(command);
    const pending = Array.isArray(raw.pending_permissions) ? raw.pending_permissions : [];
    const permission = match && pending.find((p: any) => p.id === match[2]);
    if (!match || !permission) { show('Choose an exact permission ID from the current snapshot, or refresh.'); continue; }
    // A second explicit confirmation prevents accidental approval on pasted input.
    if (match[1] === 'allow') {
      if (await ask(`Approve ONLY ${permission.id} once? Type yes: `) !== 'yes') continue;
      await client.sessions.approvePermissionOnce(session.id, permission.id, 'Explicit SDK operator approval');
    } else {
      await client.sessions.denyPermission(session.id, permission.id, 'SDK operator denied');
    }
  }
}

async function main(): Promise<void> {
  const file = process.env.SWARM_SDK_TOKEN_FILE;
  if (!file) throw new Error('Set SWARM_SDK_TOKEN_FILE to the private token JSON export');
  const handle = await open(file, constants.O_RDONLY | constants.O_NOFOLLOW);
  let token: string;
  try {
    const st = await handle.stat();
    if (!st.isFile() || (st.mode & 0o077) !== 0 || st.size > 16384) throw new Error('Token file must be a private regular file, at most 16384 bytes');
    token = JSON.parse(await handle.readFile('utf8')).token;
    if (typeof token !== 'string' || !token.startsWith('swk_')) throw new Error('Invalid scoped token file');
  } finally { await handle.close(); }
  const url = new URL(process.env.SWARM_SDK_URL ?? 'http://127.0.0.1:7783');
  if (url.protocol !== 'http:' || !['127.0.0.1', '[::1]'].includes(url.hostname) || url.username || url.password || url.pathname !== '/' || url.search || url.hash) {
    throw new Error('This example requires the loopback-published HTTP endpoint');
  }
  const client = new SwarmClient({ baseUrl: url.origin, token, timeoutMs: 15000 });
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  try {
    const prompt = await rl.question('Prompt for /project: ');
    if (!prompt.trim()) throw new Error('Prompt is required');
    await runHeadlessSession(client, prompt, q => rl.question(q), text => console.log(text));
  } finally { rl.close(); }
}

if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  main().catch(() => {
    // API/transport errors may carry private response bodies. Do not dump them.
    console.error('SDK operation failed. Check listener, token expiry/scopes and session state before retrying; details withheld.');
    process.exitCode = 1;
  });
}
