import assert from 'node:assert/strict';
import { test } from 'node:test';
import { runHeadlessSession } from '../../examples/headless-session.js';
import { SwarmClient } from '../client.js';

// Requirement: the headless example uses authenticated canonical session APIs,
// returns real snapshots, and never approves absent/unconfirmed permissions.
// An injected fetch exercises SDK serialization without a live daemon/provider;
// Go boundary tests separately prove backend authentication/account enforcement.
test('headless example requires exact permission and explicit confirmation', { timeout: 5000 }, async () => {
  const calls: { path: string; body: any }[] = [];
  const previous = globalThis.fetch;
  globalThis.fetch = async (input, init) => {
    const path = new URL(String(input)).pathname;
    assert.equal((init?.headers as any).Authorization, 'Bearer swk_fixture');
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ path, body });
    let result: any = { ok: true };
    if (path === '/v3/sessions') result = { session: { id: 'example-session' } };
    else if (path.endsWith('/messages')) result = { run_intent: { run_id: 'example-run' } };
    else if (init?.method === 'GET') result = {
      session: { id: 'example-session', state: 'running' },
      messages: [{ role: 'assistant', content: 'A result from the fixture, not a live agent.' }],
      pending_permissions: [{ id: 'permission-one', tool_name: 'bash' }],
    };
    return new Response(JSON.stringify(result), { status: 200 });
  };
  try {
    const answers = ['allow unknown', 'allow permission-one', 'no', 'allow permission-one', 'yes', 'deny permission-one', 'refresh', 'quit'];
    const shown: string[] = [];
    await runHeadlessSession(new SwarmClient({ baseUrl: 'http://127.0.0.1:7783', token: 'swk_fixture' }), 'Inspect the project', async () => {
      assert.ok(answers.length, 'unbounded interaction'); return answers.shift()!;
    }, text => shown.push(text));
    assert.equal(answers.length, 0);
    assert.equal(calls[0].body.workspace_path, '/project');
    assert.equal(calls[0].body.mode, 'auto');
    assert.equal(calls[1].body.content, 'Inspect the project');
    const resolves = calls.filter(c => c.path.endsWith('/resolve'));
    assert.deepEqual(resolves.map(c => c.body.action), ['allow_once', 'deny']);
    assert.ok(resolves.every(c => c.path.endsWith('/permissions/permission-one/resolve')));
    assert.ok(shown.some(s => s.includes('A result from the fixture')));
    assert.ok(calls.every(c => c.path.startsWith('/v3/sessions')));
  } finally { globalThis.fetch = previous; }
});
