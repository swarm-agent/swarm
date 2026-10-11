import assert from 'node:assert/strict';
import { test } from 'node:test';
import { ClientToolError, SwarmAgentsNamespace } from '../agents.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: the agents namespace must define sealed agents with only the named
// client tools (no preset that could grant built-ins), answer each pending call
// exactly once through the permission resolve route, never forward a handler's
// internal error text, and ignore calls for tools it does not serve.
// Boundary: SwarmAgentsNamespace over a transport fixture (the wire contract);
// the daemon side is covered by Go tests against the real executor.
test('agents namespace defines sealed agents and answers client tool calls once', async () => {
  const calls: Array<[string, string, unknown]> = [];
  let pending = [
    { id: 'p1', session_id: 's1', run_id: 'r1', tool_name: 'lookup_order', tool_call_arguments: '{"order_id":"AB12CD"}', created_at: 1 },
    { id: 'p2', session_id: 's1', run_id: 'r1', tool_name: 'cancel_order', tool_call_arguments: '{"order_id":"AB12CD"}', created_at: 2 },
    { id: 'p3', session_id: 's1', run_id: 'r1', tool_name: 'bash', tool_call_arguments: '{"command":"id"}', created_at: 3 },
  ];
  const transport = { request: async (path: string, options: { method?: string; body?: unknown } = {}) => {
    calls.push([options.method ?? 'GET', path, options.body]);
    if (path.includes('/permissions?status=pending')) return { data: { ok: true, permissions: pending } };
    if (path.endsWith('/resolve')) pending = pending.filter((p) => !path.includes(`/permissions/${p.id}/`));
    return { data: { ok: true, custom_tool: {}, profile: {} } };
  } } as unknown as SwarmTransport;
  const agents = new SwarmAgentsNamespace(transport);

  await agents.defineClientTool({ name: 'lookup_order', description: 'Look up', input_schema: { type: 'object' } });
  await agents.defineSealedAgent({ name: 'frontdesk', prompt: 'Help.', tools: ['lookup_order', 'cancel_order'] });
  assert.deepEqual(calls[0], ['PUT', '/v2/custom-tools/lookup_order', { kind: 'client', description: 'Look up', input_schema: { type: 'object' }, effect: 'read' }]);
  assert.deepEqual(calls[1], ['PUT', '/v2/agents/frontdesk', { mode: 'subagent', description: '', prompt: 'Help.', tool_contract: { preset: 'custom', tools: { lookup_order: { enabled: true }, cancel_order: { enabled: true } } } }]);
  await assert.rejects(agents.defineSealedAgent({ name: 'Front Desk', prompt: 'x', tools: [] }), /lowercase/);
  await assert.rejects(agents.defineClientTool({ name: '../x', description: '', input_schema: {} }), /lowercase/);

  const controller = new AbortController();
  const seen: unknown[] = [];
  const serving = agents.serve('s1', {
    lookup_order: async (args) => { seen.push(args); return { status: 'shipped' }; },
    cancel_order: async () => { throw new Error('db password=hunter2 rejected'); },
  }, { signal: controller.signal, intervalMs: 50 });
  await new Promise((resolve) => setTimeout(resolve, 200));
  controller.abort();
  await serving;

  const resolves = calls.filter(([, path]) => path.endsWith('/resolve'));
  assert.deepEqual(resolves, [
    ['POST', '/v3/sessions/s1/permissions/p1/resolve', { action: 'allow_once', reason: '', approved_arguments: { result: { status: 'shipped' } } }],
    ['POST', '/v3/sessions/s1/permissions/p2/resolve', { action: 'deny', reason: 'tool failed' }],
  ]);
  assert.deepEqual(seen, [{ order_id: 'AB12CD' }]);
  assert.equal(pending.length, 1, 'the bash record is not ours to answer');

  calls.length = 0;
  pending = [{ id: 'p4', session_id: 's1', run_id: 'r2', tool_name: 'lookup_order', tool_call_arguments: '{}', created_at: 4 }];
  const stop = new AbortController();
  const again = agents.serve('s1', { lookup_order: () => { throw new ClientToolError('order not found'); } }, { signal: stop.signal, intervalMs: 50 });
  await new Promise((resolve) => setTimeout(resolve, 120));
  stop.abort();
  await again;
  assert.deepEqual(calls.find(([, path]) => path.endsWith('/resolve')), ['POST', '/v3/sessions/s1/permissions/p4/resolve', { action: 'deny', reason: 'order not found' }]);
});

// Purpose: converse is the whole per-message loop of a gateway. It must return
// only the turn's new assistant text, answer client tool calls while the run is
// active, and fail fast (not wait for the timeout) when the run fails.
test('converse returns the new reply and fails fast on a failed run', async () => {
  const base = [{ id: 'm0', role: 'assistant', content: 'earlier' }];
  let polls = 0;
  let resolved = false;
  let failRun = false;
  let sent = false;
  const transport = { request: async (path: string, options: { method?: string; body?: any } = {}) => {
    if (path.endsWith('/messages') && options.method === 'POST') { polls = 0; sent = true; return { data: { ok: true } }; }
    if (path.includes('/permissions?status=pending')) {
      return { data: { permissions: polls > 0 && !resolved && !failRun ? [{ id: 'p1', session_id: 's1', run_id: 'r', tool_name: 'search_docs', tool_call_arguments: '{"query":"install"}', created_at: 1 }] : [] } };
    }
    if (path.endsWith('/resolve')) { resolved = true; return { data: { ok: true } }; }
    if (path === '/v3/sessions/s1') {
      polls += 1;
      if (failRun && sent && polls > 1) return { data: { session: { id: 's1' }, active_run_intent: null, pending_permissions: [], messages: [...base, { id: 'u1', role: 'user', content: 'hi' }, { id: 'f1', role: 'system', content: '[run-failed] model rejected', metadata: { message_kind: 'run_failure' } }] } };
      if (!resolved || polls < 3) return { data: { session: { id: 's1' }, active_run_intent: polls > 1 ? { run_id: 'r' } : null, pending_permissions: [], messages: base } };
      return { data: { session: { id: 's1' }, active_run_intent: null, pending_permissions: [], messages: [...base, { id: 'u1', role: 'user', content: 'hi' }, { id: 'a1', role: 'assistant', content: 'Use install.sh' }] } };
    }
    throw new Error(`unexpected ${path}`);
  } } as unknown as SwarmTransport;
  const agents = new SwarmAgentsNamespace(transport);
  const seen: unknown[] = [];
  const turn = await agents.converse('s1', 'hi', { search_docs: async (args) => { seen.push(args); return { results: [] }; } }, { pollIntervalMs: 20, timeoutMs: 5000 });
  assert.equal(turn.reply, 'Use install.sh');
  assert.deepEqual(seen, [{ query: 'install' }]);
  failRun = true;
  sent = false;
  const started = Date.now();
  await assert.rejects(agents.converse('s1', 'hi', {}, { pollIntervalMs: 20, timeoutMs: 5000 }), /run failed/);
  assert.ok(Date.now() - started < 2000, 'a failed run must not wait for the timeout');
});
