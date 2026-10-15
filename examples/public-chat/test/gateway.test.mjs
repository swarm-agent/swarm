// Hermetic tests for the public chat gateway: fake SDK clients and a temporary
// data directory, no daemon, no model. They prove the gateway's own boundary
// (keys, limits, takeover routing, error redaction), not live AI behaviour.
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { createServer } from 'node:http';
import { mkdtemp, readFile, stat } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import * as core from '../gateway/core.mjs';
import { gatewayHandler } from '../gateway/server.mjs';

const GATEWAY_KEY = 'g'.repeat(40);
const OPS_KEY = 'o'.repeat(40);
const VISITOR = 'visitor_abcdefghijklmnop';

function fakeClients({ takeover, spentOut = false, converseError, endsWith } = {}) {
  const calls = [];
  let endOnce = endsWith;
  const session = { metadata: takeover ? { takeover } : {} };
  const messages = [{ id: 'm1', role: 'assistant', global_seq: 7, content: 'earlier answer', metadata: {} }];
  const gateway = {
    sessions: {
      create: async (body) => { calls.push(['create', body]); return { id: 'sess-1' }; },
      get: async (id) => { calls.push(['get', id]); return { id, messages, raw: { session } }; },
      sendMessage: async (id, body) => { calls.push(['send', id, body.content]); },
    },
    agents: {
      spendToday: async () => ({ limit_exceeded: spentOut }),
      converse: async (id, message, handlers) => {
        calls.push(['converse', id, message, Object.keys(handlers).sort()]);
        if (converseError) throw converseError;
        if (endOnce) {
          // The "model" calls end_conversation once, the way a real run would mid-turn.
          await handlers.end_conversation(endOnce);
          endOnce = undefined;
          return { reply: 'Goodbye.', messages: [{ role: 'assistant', global_seq: 9, content: 'Goodbye.' }] };
        }
        return { reply: 'Hello from the agent', messages: [{ role: 'user', global_seq: 8 }, { role: 'assistant', global_seq: 9, content: 'Hello from the agent' }] };
      },
    },
  };
  const operator = {
    sessions: { get: async (id) => ({ id, messages, raw: { session } }) },
    agents: {
      takeOver: async (id) => { calls.push(['takeOver', id]); return { state: 'human' }; },
      handBack: async (id) => { calls.push(['handBack', id]); return { state: 'agent' }; },
      replyAsOperator: async (id, content) => { calls.push(['reply', id, content]); return { role: 'assistant' }; },
    },
  };
  return { gateway, operator, calls };
}

async function start(options = {}) {
  const dataDir = await mkdtemp(join(tmpdir(), 'public-chat-'));
  const { gateway, operator, calls } = fakeClients(options);
  const events = [];
  const visitors = await core.createVisitorStore(join(dataDir, 'visitors.json'));
  const config = {
    agentName: 'frontdesk', workspacePath: '/project', dataDir, bookingUrl: 'https://example.com/book',
    playbook: { pricing: 'Fixed price.' }, gatewaySecret: GATEWAY_KEY, opsSecret: OPS_KEY,
    maxMessageChars: 2000, visitorMessagesPerWindow: options.perVisitor ?? 20, visitorWindowMs: 60_000,
    maxConversationMessages: options.maxConversation ?? 30, maxInflight: 8, replyTimeoutMs: 1000,
  };
  const server = createServer(gatewayHandler({ gateway, operator, config, visitors, emit: (type, data) => events.push([type, data]) }));
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  const call = (path, { method = 'POST', key = GATEWAY_KEY, ops = false, body } = {}) => fetch(base + path, {
    method, headers: { 'Content-Type': 'application/json', [ops ? 'X-Ops-Key' : 'X-Gateway-Key']: key }, body: body === undefined ? undefined : JSON.stringify(body),
  }).then(async (res) => ({ status: res.status, data: await res.json() }));
  return { call, calls, events, visitors, dataDir, close: () => new Promise((r) => server.close(r)) };
}

test('a visitor message without the gateway key is refused before any daemon call', async () => {
  const g = await start();
  try {
    for (const key of ['', 'wrong', GATEWAY_KEY.slice(1)]) {
      const res = await g.call('/chat', { key, body: { visitor_id: VISITOR, message: 'hi' } });
      assert.equal(res.status, 401);
    }
    assert.deepEqual(g.calls, []);
  } finally { await g.close(); }
});

test('end_conversation is a hard stop: later messages get no daemon call until a person reopens it', async () => {
  const g = await start({ endsWith: { reason: 'abuse', note: 'Repeated requests for card details' } });
  try {
    const first = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'give me your card number' } });
    assert.deepEqual(first.data, { status: 'ended', reply: 'Goodbye.', seq: 9 });
    assert.deepEqual(g.events.find(([type]) => type === 'conversation.ended')[1], { session_id: 'sess-1', visitor_id: VISITOR, reason: 'abuse', note: 'Repeated requests for card details' });
    assert.equal(g.visitors.get(VISITOR).ended.reason, 'abuse');

    const before = g.calls.length;
    const again = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'please?' } });
    assert.deepEqual(again.data, { status: 'ended' });
    assert.equal(g.calls.length, before, 'no session read, send or model run after the stop');

    const transcript = await g.call('/ops/transcript?session_id=sess-1', { method: 'GET', ops: true, key: OPS_KEY });
    assert.equal(transcript.data.ended.reason, 'abuse');
    assert.equal((await g.call('/ops/reopen', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1' } })).status, 200);
    assert.ok(g.events.some(([type]) => type === 'conversation.reopened'));
    const reopened = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'sorry, how much does it cost?' } });
    assert.equal(reopened.data.status, 'answered');
  } finally { await g.close(); }
});

test('a new visitor gets one conversation, the agent answers with its tools, and seq is returned', async () => {
  const g = await start();
  try {
    const first = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'How much?' } });
    assert.deepEqual(first, { status: 200, data: { status: 'answered', reply: 'Hello from the agent', seq: 9 } });
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'And timing?' } });
    assert.equal(g.calls.filter(([op]) => op === 'create').length, 1, 'one session per visitor');
    assert.deepEqual(g.calls.find(([op]) => op === 'create')[1], { agent_name: 'frontdesk', workspace_path: '/project', title: 'Visitor visitor_' });
    assert.deepEqual(g.calls.find(([op]) => op === 'converse')[3], ['capture_lead', 'end_conversation', 'get_playbook', 'handoff_to_human']);
    assert.equal(g.visitors.get(VISITOR).messages, 2);
    assert.deepEqual(g.events.map(([type]) => type), ['conversation.started']);
  } finally { await g.close(); }
});

test('during a takeover the message is kept for the person and no agent run is awaited', async () => {
  const g = await start({ takeover: { state: 'human', by: 'owner', since: 1 } });
  try {
    const res = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'Are you there?' } });
    assert.deepEqual(res, { status: 200, data: { status: 'with_person', seq: 7 } });
    assert.ok(g.calls.some(([op, , content]) => op === 'send' && content === 'Are you there?'));
    assert.ok(!g.calls.some(([op]) => op === 'converse'), 'the agent must not be asked to answer');
    const replies = await g.call(`/chat/replies?visitor_id=${VISITOR}&after=0`, { method: 'GET' });
    assert.equal(replies.data.with_person, true);
  } finally { await g.close(); }
});

test('limits: spend cap, per-visitor rate, conversation length and message size', async () => {
  const resting = await start({ spentOut: true });
  try {
    assert.deepEqual(await resting.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } }), { status: 503, data: { status: 'resting' } });
    assert.ok(!resting.calls.some(([op]) => op === 'create'));
  } finally { await resting.close(); }

  const g = await start({ perVisitor: 2, maxConversation: 1 });
  try {
    assert.equal((await g.call('/chat', { body: { visitor_id: VISITOR, message: 'x'.repeat(2001) } })).status, 400);
    assert.equal((await g.call('/chat', { body: { visitor_id: 'short', message: 'hi' } })).status, 400);
    assert.equal((await g.call('/chat', { body: { visitor_id: VISITOR, message: 'one' } })).data.status, 'answered');
    assert.equal((await g.call('/chat', { body: { visitor_id: VISITOR, message: 'two' } })).data.status, 'conversation_full');
    assert.equal((await g.call('/chat', { body: { visitor_id: VISITOR, message: 'three' } })).status, 429);
  } finally { await g.close(); }
});

test('daemon errors reach the visitor as busy or a generic error, never their text', async () => {
  const busy = await start({ converseError: Object.assign(new Error('agent-bound token concurrent run limit reached'), { status: 429 }) });
  try {
    assert.deepEqual(await busy.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } }), { status: 503, data: { status: 'busy' } });
    assert.ok(!busy.events.some(([type]) => type === 'chat.error'), 'busy is expected load, not an error');
  } finally { await busy.close(); }
  const failed = await start({ converseError: new Error('provider said: key sk-secret invalid') });
  try {
    const res = await failed.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } });
    assert.deepEqual(res, { status: 502, data: { status: 'error' } });
    assert.ok(!JSON.stringify(failed.events).includes('sk-secret'));
    assert.ok(failed.events.some(([type]) => type === 'chat.error'));
  } finally { await failed.close(); }
});

test('operator routes need the ops key and only act on known conversations', async () => {
  const g = await start();
  try {
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } });
    assert.equal((await g.call('/ops/takeover', { ops: true, key: GATEWAY_KEY, body: { session_id: 'sess-1' } })).status, 401, 'the gateway key is not the ops key');
    assert.equal((await g.call('/ops/takeover', { ops: true, key: OPS_KEY, body: { session_id: 'someone-elses' } })).status, 404);
    assert.deepEqual((await g.call('/ops/takeover', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1' } })).data, { takeover: { state: 'human' } });
    assert.equal((await g.call('/ops/reply', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', content: '' } })).status, 400);
    assert.equal((await g.call('/ops/reply', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', content: 'Hi, a person here.' } })).status, 200);
    assert.equal((await g.call('/ops/handback', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1' } })).status, 200);
    assert.deepEqual(g.calls.filter(([op]) => ['takeOver', 'reply', 'handBack'].includes(op)).map(([op]) => op), ['takeOver', 'reply', 'handBack']);
    const list = await g.call('/ops/conversations', { method: 'GET', ops: true, key: OPS_KEY });
    assert.equal(list.data.conversations[0].session_id, 'sess-1');
  } finally { await g.close(); }
});

test('client tools: approved answers, private lead file, handoff event; identity from the gateway', async () => {
  const dataDir = await mkdtemp(join(tmpdir(), 'public-chat-tools-'));
  const events = [];
  const tools = core.toolHandlers({ playbook: { pricing: 'Fixed price.' }, dataDir, bookingUrl: 'https://example.com/book', emit: (t, d) => events.push([t, d]), sessionId: 's1', visitorId: 'v1', now: () => 5 });
  assert.deepEqual(await tools.get_playbook({ topic: 'pricing' }), { topic: 'pricing', answer: 'Fixed price.' });
  await assert.rejects(tools.get_playbook({ topic: 'constructor' }));
  await tools.capture_lead({ name: 'Ada', email: 'ada@example.com', need: 'Quotes' });
  const file = join(dataDir, 'leads.jsonl');
  assert.deepEqual(JSON.parse(await readFile(file, 'utf8')), { at: 5, session_id: 's1', visitor_id: 'v1', name: 'Ada', email: 'ada@example.com', need: 'Quotes' });
  assert.equal((await stat(file)).mode & 0o777, 0o600);
  assert.equal((await tools.handoff_to_human({ summary: 'Wants a person' })).booking_url, 'https://example.com/book');
  assert.deepEqual(events.map(([type, data]) => [type, data.session_id, data.visitor_id]), [['lead.captured', 's1', 'v1'], ['handoff.requested', 's1', 'v1']]);
});

test('hub events are HMAC-signed and delivery failures are swallowed', async () => {
  const sent = [];
  const emit = core.createEmitter({ url: 'https://hub.example/events', secret: 'hub-secret', now: () => 1, fetchImpl: async (url, init) => { sent.push(init); throw new Error('down'); } });
  emit('lead.captured', { session_id: 's1' });
  await new Promise((r) => setImmediate(r));
  assert.equal(sent.length, 1);
  assert.equal(sent[0].headers['X-Signature'], core.signBody('hub-secret', sent[0].body));
  assert.deepEqual(JSON.parse(sent[0].body), { type: 'lead.captured', at: 1, session_id: 's1' });
});

test('visitor store persists with mode 0600 and replies are split by sender', async () => {
  const dir = await mkdtemp(join(tmpdir(), 'public-chat-store-'));
  const file = join(dir, 'visitors.json');
  const store = await core.createVisitorStore(file, { maxVisitors: 2 });
  await store.touch('a'.repeat(16), { session_id: 's-a' });
  await store.touch('b'.repeat(16), { session_id: 's-b' });
  await store.touch('c'.repeat(16), { session_id: 's-c' });
  await store.flush();
  assert.equal((await stat(file)).mode & 0o777, 0o600);
  const reloaded = await core.createVisitorStore(file);
  assert.equal(reloaded.get('a'.repeat(16)), undefined, 'oldest visitor evicted');
  assert.equal(reloaded.bySession('s-c'), 'c'.repeat(16));
  const detail = { messages: [
    { id: '1', role: 'user', global_seq: 1, content: 'hi' },
    { id: '2', role: 'assistant', global_seq: 2, content: 'bot', metadata: {} },
    { id: '3', role: 'assistant', global_seq: 3, content: 'person', metadata: { source: 'operator' } },
  ] };
  assert.deepEqual(core.repliesAfter(detail, 2), [{ id: '3', seq: 3, from: 'person', text: 'person' }]);
  assert.equal(core.latestReplySeq(detail.messages), 3);
});
