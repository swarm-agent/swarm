import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmAgentsNamespace } from '../agents.js';
import { SwarmApiError, SwarmForbiddenError, SwarmNotFoundError } from '../errors.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: the gateway and operator keys must always be minted with an expiry
// and the operator flag only where intended, and takeover / operator replies
// must go to their own routes with an idempotency key, so a retried reply is
// recorded once. Boundary: SwarmAgentsNamespace over a transport fixture; the
// daemon side (who may call these routes) is covered by Go tests.
test('gateway, operator and takeover calls use the reviewed wire shape', async () => {
  const calls: Array<[string, string, Record<string, unknown> | undefined]> = [];
  const transport = { request: async (path: string, options: { method?: string; body?: Record<string, unknown> } = {}) => {
    calls.push([options.method ?? 'GET', path, options.body]);
    if (path.endsWith('/takeover')) return { data: { ok: true, takeover: { state: options.method === 'POST' && options.body?.action === 'start' ? 'human' : 'agent' } } };
    if (path.endsWith('/operator-messages')) return { data: { ok: true, message: { role: 'assistant' } } };
    return { data: { token: 'swk_x', record: {} } };
  } } as unknown as SwarmTransport;
  const agents = new SwarmAgentsNamespace(transport);

  await agents.createGatewayToken('frontdesk', { expiresInSeconds: 3600, maxConcurrentRuns: 2 });
  await agents.createOperatorToken('frontdesk', { expiresInSeconds: 3600 });
  assert.deepEqual(calls[0], ['POST', '/v3/auth/tokens', { name: 'frontdesk gateway', agent_name: 'frontdesk', expires_in_seconds: 3600, max_concurrent_runs: 2 }]);
  assert.deepEqual(calls[1], ['POST', '/v3/auth/tokens', { name: 'frontdesk operator', agent_name: 'frontdesk', operator: true, expires_in_seconds: 3600 }]);
  await agents.createGatewayToken('frontdesk');
  assert.deepEqual(calls.pop(), ['POST', '/v3/auth/tokens', { name: 'frontdesk gateway', agent_name: 'frontdesk', expires_in_seconds: undefined }]);

  assert.equal((await agents.takeOver('s/1', { clientRequestId: 'take-1' })).state, 'human');
  await agents.replyAsOperator('s/1', 'Hi, a person here.', { clientRequestId: 'reply-1' });
  assert.equal((await agents.handBack('s/1')).state, 'agent');
  assert.deepEqual(calls[2], ['POST', '/v3/sessions/s%2F1/takeover', { action: 'start', client_request_id: 'take-1' }]);
  assert.deepEqual(calls[3], ['POST', '/v3/sessions/s%2F1/operator-messages', { content: 'Hi, a person here.', client_request_id: 'reply-1' }]);
  assert.equal(calls[4][1], '/v3/sessions/s%2F1/takeover');
  assert.equal(calls[4][2]?.action, 'end');
  assert.match(String(calls[4][2]?.client_request_id), /^sdk-operator-/);
});

// Purpose: a gateway names the end user when it creates a conversation and
// gets that end user back with every client tool call, from the delivery the
// daemon builds from the stored session, never from the model's arguments;
// serve/converse with `endUser` refuse to act on another end user's session;
// the per-end-user limits are sent on mint and on update with the daemon's
// field names. Boundary: SwarmAgentsNamespace over a transport fixture; the
// daemon's own enforcement is covered by Go tests.
test('end user on create, delivery, serve, converse and token limits', async () => {
  const calls: Array<[string, string, Record<string, unknown> | undefined]> = [];
  const resolved: Array<[string, Record<string, unknown> | undefined]> = [];
  let sessionEndUser: string | undefined = 'cust-42';
  const transport = { request: async (path: string, options: { method?: string; body?: Record<string, unknown> } = {}) => {
    calls.push([options.method ?? 'GET', path, options.body]);
    if (path === '/v3/sessions' && options.method === 'POST') {
      return { data: { ok: true, session: { id: 's1', title: 'Visitor' }, end_user: options.body?.end_user, labels: options.body?.labels } };
    }
    if (path.startsWith('/v3/sessions/s1/wait')) {
      // A wait with a cursor blocks on the daemon; nothing changes here.
      if (path.includes('after=')) await new Promise((resolve) => setTimeout(resolve, 5));
      return { data: { ok: true, session_id: 's1', cursor: 1, reasons: [], ...(sessionEndUser ? { end_user: sessionEndUser, labels: { plan: 'pro' } } : {}), pending_calls: [
        { id: 'p1', session_id: 's1', run_id: 'r1', tool_name: 'lookup_order', tool_call_arguments: '{"order_id":"ABC123","end_user":"mallory"}', requirement: 'client_tool', mode: 'auto', status: 'pending', created_at: 1, updated_at: 1 },
      ] } };
    }
    if (path === '/v3/sessions/s1/confirmations') return { data: { ok: true, confirmations: [] } };
    if (path.endsWith('/resolve')) { resolved.push([path, options.body]); return { data: { ok: true } }; }
    if (path === '/v3/sessions/s1' && (options.method ?? 'GET') === 'GET') {
      return { data: { session: { id: 's1' }, messages: [], ...(sessionEndUser ? { end_user: sessionEndUser } : {}) } };
    }
    if (path.startsWith('/v3/sessions:end-user-usage')) {
      return { data: { ok: true, date: '2026-10-16', truncated: false, end_users: [{ end_user: 'cust-42', recorded: true, usage: { total_tokens: 110, input_tokens: 90, output_tokens: 20, receipt_count: 2 } }] } };
    }
    return { data: { token: 'swk_x', record: { id: 'tok_1' } } };
  } } as unknown as SwarmTransport;
  const agents = new SwarmAgentsNamespace(transport);

  const started = await agents.startConversation('frontdesk', { endUser: 'cust-42', labels: { plan: 'pro' }, clientRequestId: 'visit-1' });
  assert.deepEqual(started, { sessionId: 's1', endUser: 'cust-42', labels: { plan: 'pro' } });
  assert.equal(calls[0][2]?.end_user, 'cust-42');
  assert.deepEqual(calls[0][2]?.labels, { plan: 'pro' });
  assert.equal(calls[0][2]?.client_request_id, 'visit-1');

  const pending = await agents.pendingCalls('s1', ['lookup_order']);
  assert.equal(pending.length, 1);
  assert.equal(pending[0].endUser, 'cust-42', 'the end user comes from the delivery, not the model arguments');
  assert.deepEqual(pending[0].labels, { plan: 'pro' });
  assert.equal(pending[0].arguments.end_user, 'mallory');

  // serve with another end user fails the call without running the handler.
  let ran = 0;
  const controller = new AbortController();
  const serving = agents.serve('s1', { lookup_order: () => { ran++; return { ok: true }; } }, { signal: controller.signal, intervalMs: 50, endUser: 'cust-7' });
  await new Promise((resolve) => setTimeout(resolve, 20));
  controller.abort();
  await serving;
  assert.equal(ran, 0);
  assert.equal(resolved[0][1]?.action, 'deny');

  await assert.rejects(agents.converse('s1', 'hi', {}, { endUser: 'cust-7' }), /another end user/);
  sessionEndUser = undefined;
  await assert.rejects(agents.converse('s1', 'hi', {}, { endUser: 'cust-42' }), /another end user/);
  assert.equal(calls.filter(([method, path]) => method === 'POST' && path === '/v3/sessions/s1/messages').length, 0, 'converse sent a message to the wrong end user');

  await agents.createGatewayToken('frontdesk', { expiresInSeconds: 3600, endUserMessagesPerMinute: 10, endUserSessionsPerHour: 5, endUserMaxTokensPerDay: 20000 });
  const mint = calls.find(([, path, body]) => path === '/v3/auth/tokens' && body?.end_user_messages_per_minute !== undefined);
  assert.deepEqual(mint?.[2], { name: 'frontdesk gateway', agent_name: 'frontdesk', expires_in_seconds: 3600, end_user_messages_per_minute: 10, end_user_sessions_per_hour: 5, end_user_max_tokens_per_day: 20000 });
  await agents.updateGatewayTokenLimits('tok/1', { endUserMaxTokensPerDay: 0, messagesPerMinute: 60 });
  assert.deepEqual(calls[calls.length - 1], ['POST', '/v3/auth/tokens/tok%2F1/limits', { messages_per_minute: 60, end_user_max_tokens_per_day: 0 }]);
  await assert.rejects(agents.updateGatewayTokenLimits('tok_1', {}), /at least one limit/);
  await assert.rejects(agents.createGatewayToken('frontdesk', { expiresInSeconds: 3600, endUserMessagesPerMinute: -1 }), /non-negative/);

  const usage = await agents.endUserUsage({ endUser: 'cust-42', date: '2026-10-16' });
  assert.equal(calls[calls.length - 1][1], '/v3/sessions:end-user-usage?end_user=cust-42&date=2026-10-16');
  assert.deepEqual(usage.endUsers.map((row) => [row.endUser, row.usage.total_tokens]), [['cust-42', 110]]);
});

const confirmationRecord = {
  id: 'conf_1', kind: 'confirmation', status: 'pending', tool: 'place_order', summary: 'Order 2 of AB12CD',
  arguments: { order_id: 'AB12CD', quantity: 2 }, arguments_hash: 'sha256:abc', run_id: 'run-1', call_id: 'write1',
  created_at: 1, expires_at: 2, execution_call_id: 'confirmed_1',
};

// Purpose: a confirmed tool call reaches the gateway only as a confirmation
// (kind "confirmation"), never as something serve() answers; the gateway's
// approval carries the shown arguments' hash, and the approved call is then
// answered by the handler with the confirmation's exact arguments. Boundary:
// SwarmAgentsNamespace.pendingCalls/serve/converse/confirm/reject over a
// scripted transport; the daemon's half is covered by Go tests.
test('confirmations are surfaced, never auto-answered, and decided with the arguments hash', async () => {
  const calls: Array<[string, string, Record<string, any> | undefined]> = [];
  let phase: 'asked' | 'approved' | 'answered' = 'asked';
  let cursor = 0;
  const messages: Array<Record<string, unknown>> = [];
  const transport = { request: async (path: string, options: { method?: string; body?: Record<string, any> } = {}) => {
    const method = options.method ?? 'GET';
    calls.push([method, path, options.body]);
    if (path === '/v2/custom-tools/place_order') return { data: { custom_tool: {} } };
    if (method === 'POST' && path.endsWith('/messages')) {
      messages.push({ id: 'u1', role: 'user', content: options.body?.content });
      return { data: {} };
    }
    if (path.startsWith('/v3/sessions/s1/wait')) {
      cursor++;
      const asked = phase === 'asked' && messages.length > 0;
      const pending_calls = phase === 'approved'
        ? [{ id: 'perm-x', session_id: 's1', run_id: '', tool_name: 'place_order', tool_arguments: JSON.stringify(confirmationRecord.arguments), created_at: 3 }]
        : [];
      const replied = messages.some((m) => m.role === 'assistant');
      const reasons = pending_calls.length ? ['tool_call'] : asked ? ['confirmation', 'run_ended'] : replied ? ['assistant_message', 'run_ended'] : [];
      return { data: { session_id: 's1', cursor, reasons, pending_calls, pending_confirmations: asked ? [confirmationRecord] : [] } };
    }
    if (path === '/v3/sessions/s1/confirmations') return { data: { confirmations: phase === 'asked' && messages.length > 0 ? [confirmationRecord] : [] } };
    if (method === 'POST' && path === '/v3/sessions/s1/confirmations/conf_1') {
      if (options.body?.decision === 'approve') phase = 'approved';
      if (options.body?.decision === 'reject') { phase = 'answered'; messages.push({ id: 'a2', role: 'assistant', content: 'Okay, I will not order it.' }); }
      return { data: { confirmation: { ...confirmationRecord, status: options.body?.decision === 'approve' ? 'approved' : 'rejected' } } };
    }
    if (method === 'POST' && path === '/v3/sessions/s1/permissions/perm-x/resolve') {
      phase = 'answered';
      messages.push({ id: 'a1', role: 'assistant', content: 'Your order is placed.' });
      return { data: {} };
    }
    if (path === '/v3/sessions/s1') {
      const pending_confirmation = phase === 'asked' && messages.length > 0 ? confirmationRecord : undefined;
      const pending_permissions = phase === 'approved' ? [{ id: 'perm-x' }] : [];
      return { data: { session: { id: 's1' }, messages: [...messages], pending_permissions, ...(pending_confirmation ? { pending_confirmation } : {}) } };
    }
    throw new Error(`unexpected ${method} ${path}`);
  } } as unknown as SwarmTransport;
  const agents = new SwarmAgentsNamespace(transport);

  await agents.defineClientTool({ name: 'place_order', description: 'x', input_schema: { type: 'object' }, effect: 'write', confirm: true, confirm_timeout_ms: 60000 });
  assert.deepEqual(calls[0][2], { kind: 'client', description: 'x', input_schema: { type: 'object' }, effect: 'write', confirm: true, confirm_timeout_ms: 60000 });

  const executed: Array<Record<string, unknown>> = [];
  const handlers = { place_order: async (args: Record<string, unknown>) => { executed.push(args); return { status: 'placed' }; } };
  const seen: string[] = [];
  const turn = await agents.converse('s1', 'order 2 of AB12CD', handlers, { pollIntervalMs: 5, onConfirm: (c) => { seen.push(c.id); } });
  assert.equal(turn.confirmation?.kind, 'confirmation');
  assert.equal(turn.confirmation?.argumentsHash, 'sha256:abc');
  assert.equal(turn.confirmation?.summary, 'Order 2 of AB12CD');
  assert.equal(executed.length, 0, 'nothing may run before the person approves');
  assert.ok(!calls.some(([m, p]) => m === 'POST' && (p.includes('/resolve') || p.includes('/confirmations/'))), 'serve answered or decided a confirmation on its own');

  const pending = await agents.pendingCalls('s1', ['place_order']);
  assert.deepEqual(pending.map((item) => item.kind), ['confirmation']);
  assert.ok(!calls.some(([, p]) => p === '/v3/sessions/s1/confirmations' || p.includes('/permissions?')), 'converse or pendingCalls polled instead of waiting');
  assert.deepEqual(await agents.pendingCalls('s1', ['other_tool']), []);

  await assert.rejects(agents.decideConfirmation('s1', 'conf_1', { decision: 'approve' }), /argumentsHash/);
  const done = await agents.confirm('s1', turn.confirmation!, handlers, { pollIntervalMs: 5 });
  const decision = calls.find(([m, p]) => m === 'POST' && p === '/v3/sessions/s1/confirmations/conf_1');
  assert.deepEqual(decision?.[2], { decision: 'approve', arguments_hash: 'sha256:abc' });
  assert.deepEqual(executed, [{ order_id: 'AB12CD', quantity: 2 }]);
  assert.equal(done.reply, 'Your order is placed.');
  assert.equal(done.confirmation, undefined);

  phase = 'asked';
  const declinedTurn = await agents.reject('s1', turn.confirmation!, handlers, { note: 'too expensive', pollIntervalMs: 5 });
  assert.equal(declinedTurn.reply, 'Okay, I will not order it.');
  const declined = calls.filter(([m, p]) => m === 'POST' && p === '/v3/sessions/s1/confirmations/conf_1').at(-1);
  assert.deepEqual(declined?.[2], { decision: 'reject', note: 'too expensive' });
});

// Purpose: a confirmation on an end user's session carries that end user and
// labels (from the daemon's response, as on calls); serve with another
// `endUser` never shows its card, and confirm/reject with another `endUser`
// refuse before recording any decision. Boundary: SwarmAgentsNamespace over a
// transport fixture; the daemon side is TestConfirmationOnEndUserSessionCarriesTheEndUser.
test('confirmations carry the session end user and respect endUser', async () => {
  const calls: Array<[string, string, Record<string, unknown> | undefined]> = [];
  const transport = { request: async (path: string, options: { method?: string; body?: Record<string, unknown> } = {}) => {
    const method = options.method ?? 'GET';
    calls.push([method, path, options.body]);
    if (path.startsWith('/v3/sessions/s1/wait')) {
      // A wait with a cursor blocks on the daemon; nothing changes here.
      if (path.includes('after=')) await new Promise((resolve) => setTimeout(resolve, 5));
      return { data: { session_id: 's1', cursor: 1, reasons: [], pending_calls: [], pending_confirmations: [confirmationRecord], end_user: 'cust-42', labels: { plan: 'pro' } } };
    }
    if (path === '/v3/sessions/s1/confirmations') return { data: { confirmations: [confirmationRecord], end_user: 'cust-42', labels: { plan: 'pro' } } };
    if (path === '/v3/sessions/s1') return { data: { session: { id: 's1' }, messages: [], end_user: 'cust-42', pending_confirmation: confirmationRecord } };
    throw new Error(`unexpected ${method} ${path}`);
  } } as unknown as SwarmTransport;
  const agents = new SwarmAgentsNamespace(transport);

  const [item] = await agents.pendingCalls('s1', ['place_order']);
  assert.equal(item.kind, 'confirmation');
  assert.equal(item.endUser, 'cust-42');
  assert.deepEqual(item.labels, { plan: 'pro' });

  const seen: string[] = [];
  for (const endUser of ['cust-7', 'cust-42']) {
    const controller = new AbortController();
    const serving = agents.serve('s1', { place_order: () => ({}) }, { signal: controller.signal, intervalMs: 50, endUser, onConfirm: (c) => { seen.push(`${endUser}:${c.id}`); } });
    await new Promise((resolve) => setTimeout(resolve, 20));
    controller.abort();
    await serving;
  }
  assert.deepEqual(seen, ['cust-42:conf_1'], 'serve showed another end user\'s confirmation');

  const card = item as Extract<typeof item, { kind: 'confirmation' }>;
  await assert.rejects(agents.confirm('s1', card, {}, { endUser: 'cust-7' }), /another end user/);
  await assert.rejects(agents.reject('s1', card, {}, { endUser: 'cust-7', note: 'no' }), /another end user/);
  assert.ok(!calls.some(([method, path]) => method === 'POST'), 'a decision was sent for another end user');
  assert.ok(!calls.some(([, path]) => path.endsWith('/confirmations')), 'confirmations were polled although the wait carries them');
});

type Call = { method: string; path: string; body?: Record<string, unknown> };

function fixtureTransport(route: (call: Call) => unknown): { transport: SwarmTransport; calls: Call[] } {
  const calls: Call[] = [];
  const transport = { request: async (path: string, options: { method?: string; body?: Record<string, unknown> } = {}) => {
    const call = { method: options.method ?? 'GET', path, body: options.body };
    calls.push(call);
    return { data: await route(call) };
  } } as unknown as SwarmTransport;
  return { transport, calls };
}

const pendingLookup = { id: 'perm-1', session_id: 's1', run_id: 'run-1', tool_name: 'lookup_order', tool_arguments: '{"order_id":"AB12CD"}', requirement: 'client', mode: 'ask', status: 'pending', created_at: 1, updated_at: 1 };

// Purpose: converse waits on the daemon instead of polling: one immediate wait
// for the cursor, then waits that each pass the previous cursor and a bounded
// wait_seconds; it answers the tool call the wait reports and returns the reply
// once a wait reports the run ended with nothing pending. It never reads the
// permissions route. Boundary: SwarmAgentsNamespace.converse/wait over a
// transport fixture that plays the daemon; the daemon's wake-ups are covered
// by Go tests.
test('converse waits for the next step and answers tool calls from the wait', async () => {
  let waits = 0;
  const { transport, calls } = fixtureTransport(({ method, path }) => {
    if (path.startsWith('/v3/sessions/s1/wait')) {
      waits++;
      if (waits === 1) return { session_id: 's1', cursor: 5, reasons: [], pending_calls: [] };
      if (waits === 2) return { session_id: 's1', cursor: 9, reasons: ['tool_call'], run: { run_id: 'run-1', status: 'running', active: true }, pending_calls: [pendingLookup] };
      return { session_id: 's1', cursor: 14, reasons: ['assistant_message', 'run_ended'], run: { run_id: 'run-1', status: 'completed', active: false }, pending_calls: [] };
    }
    if (path === '/v3/sessions/s1' && method === 'GET') {
      return { session: { id: 's1' }, messages: waits < 2 ? [{ id: 'm0', role: 'user', content: 'earlier' }] : [{ id: 'm0', role: 'user' }, { id: 'm1', role: 'user', content: 'where?' }, { id: 'm2', role: 'assistant', content: 'It ships tomorrow.' }] };
    }
    return { ok: true };
  });
  const agents = new SwarmAgentsNamespace(transport);
  const seen: unknown[] = [];
  const result = await agents.converse('s1', 'where?', { lookup_order: (args) => { seen.push(args); return { status: 'shipped' }; } });
  assert.equal(result.reply, 'It ships tomorrow.');
  assert.deepEqual(seen, [{ order_id: 'AB12CD' }]);
  const waitPaths = calls.filter((c) => c.path.includes('/wait')).map((c) => c.path);
  assert.deepEqual(waitPaths, ['/v3/sessions/s1/wait', '/v3/sessions/s1/wait?after=5&wait_seconds=25', '/v3/sessions/s1/wait?after=9&wait_seconds=25']);
  const resolve = calls.find((c) => c.path === '/v3/sessions/s1/permissions/perm-1/resolve');
  assert.deepEqual(resolve?.body, { action: 'allow_once', reason: '', approved_arguments: { result: { status: 'shipped' } } });
  assert.equal(calls.some((c) => c.path.includes('/permissions?')), false, 'converse polled the permissions route');
});

// Purpose: only a daemon that predates the wait route makes the SDK poll, and
// it says so once in a debug log; any other wait error is thrown, never
// silently replaced by polling. The older daemon is recognised exactly: 404,
// or its unknown-route refusals (403 for a gateway token). Boundary:
// daemonHasNoWait via pendingCalls and serve.
test('only an older daemon without the wait route falls back to polling', async () => {
  const debug: unknown[] = [];
  const original = console.debug;
  console.debug = (...args: unknown[]) => { debug.push(args); };
  try {
    const older = fixtureTransport(({ path }) => {
      if (path.includes('/wait')) throw new SwarmForbiddenError('route unavailable to an agent-bound token', {});
      if (path.includes('/permissions?status=pending')) return { permissions: [pendingLookup] };
      if (path.endsWith('/confirmations')) return { confirmations: [confirmationRecord] };
      return { ok: true };
    });
    const agents = new SwarmAgentsNamespace(older.transport);
    assert.deepEqual((await agents.pendingCalls('s1', ['lookup_order', 'place_order'])).map((item) => item.kind), ['call', 'confirmation'], 'polling still surfaces confirmations');
    assert.equal((await agents.pendingCalls('s1', ['lookup_order'])).length, 1);
    assert.equal(debug.length, 1, 'the fallback should be logged once');

    for (const error of [new SwarmForbiddenError('session not found', {}), new SwarmNotFoundError('session not found', {}), new SwarmApiError('boom', { status: 500 })]) {
      const failing = fixtureTransport(({ path }) => { if (path.includes('/wait')) throw error; return { permissions: [] }; });
      const other = new SwarmAgentsNamespace(failing.transport);
      await assert.rejects(other.pendingCalls('s1', ['lookup_order']), (thrown) => thrown === error);
      await assert.rejects(other.serve('s1', {}, {}), (thrown) => thrown === error);
      assert.equal(failing.calls.some((c) => c.path.includes('/permissions?')), false, `${error.message} fell back to polling`);
    }
  } finally {
    console.debug = original;
  }
});

// Purpose: the lifecycle and feed calls use the reviewed routes and fields:
// paged listing filters, end with an idempotency key, the existing hard
// delete, and the event feed's cursor, kinds and wait. watchEvents surfaces a
// gap and then the events, waiting (not polling) between reads. Boundary:
// SwarmAgentsNamespace over a transport fixture.
test('session lifecycle and event feed calls use the reviewed wire shape', async () => {
  let feedReads = 0;
  const controller = new AbortController();
  const { transport, calls } = fixtureTransport(({ path }) => {
    if (path.startsWith('/v3/sessions:agent?')) return { sessions: [{ id: 's1', state: 'ended' }], next_cursor: 'c2' };
    if (path.endsWith('/end')) return { ended: { at: 1, by: 'u' }, already_ended: false, stopped_run_id: 'run-1' };
    if (path.startsWith('/v3/sessions:agent-events')) {
      feedReads++;
      if (feedReads === 1) return { events: [{ seq: 12, id: 'e', at: 1, kind: 'message.user', session_id: 's1' }], next_after: 12, latest_seq: 12, oldest_seq: 10, gap: true };
      controller.abort();
      return { events: [], next_after: 12, latest_seq: 12, oldest_seq: 10, gap: false };
    }
    return { ok: true };
  });
  const agents = new SwarmAgentsNamespace(transport);
  const listed = await agents.listSessions({ state: 'ended', updatedAfter: 100, limit: 10, cursor: 'c1' });
  assert.equal(listed.nextCursor, 'c2');
  assert.equal(calls[0].path, '/v3/sessions:agent?state=ended&updated_after=100&cursor=c1&limit=10');
  const ended = await agents.endSession('s/1', { reason: 'visitor left', clientRequestId: 'end-1' });
  assert.equal(ended.stoppedRunId, 'run-1');
  assert.deepEqual(calls[1], { method: 'POST', path: '/v3/sessions/s%2F1/end', body: { client_request_id: 'end-1', reason: 'visitor left' } });
  await agents.deleteSession('s/1');
  assert.deepEqual(calls[2], { method: 'DELETE', path: '/v3/sessions/s%2F1', body: undefined });
  await agents.events({ after: 3, kinds: ['message', 'tool.failed'], waitSeconds: 40 });
  assert.equal(calls[3].path, '/v3/sessions:agent-events?after=3&kind=message%2Ctool.failed&wait=30');

  feedReads = 0;
  const seen: string[] = [];
  for await (const item of agents.watchEvents({ after: 3, signal: controller.signal })) seen.push(item.kind);
  assert.deepEqual(seen, ['gap', 'message.user']);
  const watchPaths = calls.slice(4).map((c) => c.path);
  assert.deepEqual(watchPaths, ['/v3/sessions:agent-events?after=3&wait=25', '/v3/sessions:agent-events?after=12&wait=25']);
});
