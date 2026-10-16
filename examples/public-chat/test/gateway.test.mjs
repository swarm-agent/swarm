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

function fakeClients({ takeover, spentOut = false, converseError, endsWith, offerWith, proposeWith, busyTimes = 0 } = {}) {
  const calls = [];
  let endOnce = endsWith;
  let busyLeft = busyTimes;
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
        if (busyLeft-- > 0) throw Object.assign(new Error('agent-bound token concurrent run limit reached'), { status: 429 });
        if (endOnce) {
          // The "model" calls end_conversation once, the way a real run would mid-turn.
          await handlers.end_conversation(endOnce);
          endOnce = undefined;
          return { reply: 'Goodbye.', messages: [{ role: 'assistant', global_seq: 9, content: 'Goodbye.' }] };
        }
        if (proposeWith) {
          // The "model" fills in the lead card for the visitor to check.
          await handlers.propose_lead(proposeWith);
          return { reply: 'Please check the card and press Send.', messages: [{ role: 'assistant', global_seq: 9, content: 'Please check the card and press Send.' }] };
        }
        if (offerWith) {
          // The "model" offers a person; what it says depends on the tool's answer.
          const offer = await handlers.offer_person(offerWith);
          const reply = offer.available ? `Would you like to talk to ${offer.name}?` : 'Nobody is online.';
          return { reply, messages: [{ role: 'assistant', global_seq: 9, content: reply }] };
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
      replyAsOperator: async (id, content) => {
        calls.push(['reply', id, content]);
        const message = { id: 'op' + messages.length, role: 'assistant', global_seq: 20 + messages.length, content, metadata: { source: 'operator' } };
        messages.push(message);
        return message;
      },
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
    acceptTimeoutMs: 120_000, busyWaitMs: 300, busyRetryMs: 20, agentLabel: options.agentLabel,
  };
  const now = options.now ?? Date.now;
  const presence = core.createPresence({ ttlMs: 60_000, now });
  const server = createServer(gatewayHandler({ gateway, operator, config, visitors, emit: (type, data) => events.push([type, data]), presence, now }));
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${server.address().port}`;
  const call = (path, { method = 'POST', key = GATEWAY_KEY, ops = false, body, headers = {} } = {}) => fetch(base + path, {
    method, headers: { 'Content-Type': 'application/json', [ops ? 'X-Ops-Key' : 'X-Gateway-Key']: key, ...headers }, body: body === undefined ? undefined : JSON.stringify(body),
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
    assert.deepEqual(first.data, { status: 'ended', reply: 'Goodbye.', seq: 9, person: null });
    assert.deepEqual(g.events.find(([type]) => type === 'conversation.ended')[1], { session_id: 'sess-1', visitor_id: VISITOR, reason: 'abuse', note: 'Repeated requests for card details' });
    assert.equal(g.visitors.get(VISITOR).ended.reason, 'abuse');

    const before = g.calls.length;
    const again = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'please?' } });
    assert.deepEqual(again.data, { status: 'ended', reason: 'abuse' });
    assert.equal(g.calls.length, before, 'no session read, send or model run after the stop');

    const transcript = await g.call('/ops/transcript?session_id=sess-1', { method: 'GET', ops: true, key: OPS_KEY });
    assert.equal(transcript.data.ended.reason, 'abuse');
    assert.equal((await g.call('/ops/reopen', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1' } })).status, 200);
    assert.ok(g.events.some(([type]) => type === 'conversation.reopened'));
    const creates = g.calls.filter(([op]) => op === 'create').length;
    const reopened = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'sorry, how much does it cost?' } });
    assert.equal(reopened.data.status, 'answered');
    assert.equal(g.calls.filter(([op]) => op === 'create').length, creates + 1, 'a reopened chat starts a fresh conversation');
    assert.equal(g.visitors.get(VISITOR).previous_session_id, 'sess-1');
  } finally { await g.close(); }
});

test('a person is offered only when someone is online; Yes queues the visitor and Accept joins them', async () => {
  const g = await start({ offerWith: { summary: 'Clinic booking project' } });
  const ops = (path, body, method = 'POST') => g.call(path, { method, ops: true, key: OPS_KEY, body });
  try {
    const away = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'Can I talk to someone?' } });
    assert.deepEqual([away.data.reply, away.data.person], ['Nobody is online.', null]);
    assert.deepEqual((await g.call('/chat/status', { method: 'GET' })).data, { staff_online: false, staff: [], agent_label: 'AI worker' });

    assert.equal((await ops('/ops/presence', { id: 'staff-1', name: 'Sam', role: 'founder', available: true })).status, 200);
    assert.deepEqual((await g.call('/chat/status', { method: 'GET' })).data, { staff_online: true, staff: [{ name: 'Sam', role: 'founder' }], agent_label: 'AI worker' });
    const offered = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'Can I talk to someone?' } });
    assert.deepEqual(offered.data.person, { state: 'offered', name: 'Sam', role: 'founder' });

    const runs = g.calls.filter(([op]) => op === 'converse').length;
    const yes = await g.call('/chat/person', { body: { visitor_id: VISITOR, accept: true } });
    assert.deepEqual(yes.data, { status: 'waiting', person: { state: 'waiting', name: 'Sam', role: 'founder' } });
    assert.equal(g.calls.filter(([op]) => op === 'converse').length, runs, 'pressing Yes starts no model run');
    const queue = (await ops('/ops/queue', undefined, 'GET')).data.waiting;
    assert.deepEqual(queue.map((q) => [q.session_id, q.summary]), [['sess-1', 'Clinic booking project']]);

    const feed = (await g.call('/ops/events?after=0&wait=0', { method: 'GET', ops: true, key: OPS_KEY })).data;
    assert.ok(feed.events.some((e) => e.type === 'handoff.requested' && e.source === 'button' && e.session_id === 'sess-1'));

    const accepted = await ops('/ops/accept', { session_id: 'sess-1' });
    assert.deepEqual(accepted.data.person, { state: 'joined', name: 'Sam', role: 'founder' });
    assert.ok(g.calls.some(([op, id]) => op === 'takeOver' && id === 'sess-1'));
    assert.equal((await g.call(`/chat/replies?visitor_id=${VISITOR}&after=0`, { method: 'GET' })).data.person.state, 'joined');
    assert.deepEqual((await ops('/ops/queue', undefined, 'GET')).data.waiting, []);

    await ops('/ops/handback', { session_id: 'sess-1' });
    assert.equal(g.visitors.get(VISITOR).person, null);
    assert.ok(!JSON.stringify((await g.call(`/chat/replies?visitor_id=${VISITOR}&after=0`, { method: 'GET' })).data).includes('Clinic'), 'summaries never reach the visitor');
  } finally { await g.close(); }
});

test('an unanswered request becomes missed once, and presence lapses without a sign-out', async () => {
  const clock = { t: Date.now() };
  const g = await start({ offerWith: { summary: 'Wants a call' }, now: () => clock.t });
  try {
    await g.call('/ops/presence', { ops: true, key: OPS_KEY, body: { id: 'staff-1', name: 'Sam', role: 'founder', available: true } });
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } });
    assert.equal((await g.call('/chat/person', { body: { visitor_id: VISITOR, accept: true } })).data.status, 'waiting');
    clock.t += 121_000;
    for (let i = 0; i < 2; i++) {
      assert.equal((await g.call(`/chat/replies?visitor_id=${VISITOR}&after=0`, { method: 'GET' })).data.person.state, 'missed');
    }
    assert.equal(g.events.filter(([type]) => type === 'handoff.missed').length, 1);
    assert.equal((await g.call('/chat/status', { method: 'GET' })).data.staff_online, false, 'no heartbeat for over a minute');
    assert.equal((await g.call('/chat/person', { body: { visitor_id: VISITOR, accept: true } })).data.status, 'unavailable');
  } finally { await g.close(); }
});

test('toggling No / Yes cannot page staff more than three times', async () => {
  const g = await start({ offerWith: { summary: 'Wants a call' } });
  try {
    await g.call('/ops/presence', { ops: true, key: OPS_KEY, body: { id: 'staff-1', name: 'Sam', role: 'founder', available: true } });
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } });
    const answer = async (accept) => (await g.call('/chat/person', { body: { visitor_id: VISITOR, accept } })).data.status;
    for (let i = 0; i < 3; i++) { assert.equal(await answer(true), 'waiting'); assert.equal(await answer(false), 'declined'); }
    assert.equal(await answer(true), 'slow_down');
    assert.equal(g.events.filter(([type]) => type === 'handoff.requested').length, 3);
  } finally { await g.close(); }
});

test('staff routes need the ops key and valid input; the event feed wakes on new events', async () => {
  const g = await start();
  try {
    assert.equal((await g.call('/ops/events?after=0&wait=0', { method: 'GET' })).status, 401, 'the gateway key is not an ops key');
    assert.equal((await g.call('/ops/presence', { ops: true, key: OPS_KEY, body: { id: 'x', name: '', role: 'founder', available: true } })).status, 400);
    assert.equal((await g.call('/chat/person', { body: { visitor_id: VISITOR, accept: 'yes' } })).status, 400);
    assert.equal((await g.call('/chat/person', { body: { visitor_id: VISITOR, accept: true } })).status, 404, 'unknown visitor');
  } finally { await g.close(); }
  const feed = core.createFeed();
  const started = Date.now();
  const waiting = feed.wait(0, 5000);
  feed.push('handoff.requested', { session_id: 's1' });
  assert.deepEqual((await waiting).map((e) => [e.seq, e.type]), [[1, 'handoff.requested']]);
  assert.ok(Date.now() - started < 1000, 'resolved on push, not on timeout');
});

test('when every run slot is taken the visitor waits for one instead of being turned away', async () => {
  const g = await start({ busyTimes: 3 });
  try {
    const res = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } });
    assert.equal(res.data.status, 'answered');
    assert.equal(g.calls.filter(([op]) => op === 'converse').length, 4, 'three refusals, then the run');
  } finally { await g.close(); }
});

test('after a refresh the visitor gets their own history back, and only theirs', async () => {
  const g = await start();
  try {
    assert.deepEqual((await g.call(`/chat/history?visitor_id=${VISITOR}`, { method: 'GET' })).data, { messages: [] });
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } });
    const h = (await g.call(`/chat/history?visitor_id=${VISITOR}`, { method: 'GET' })).data;
    assert.deepEqual(h.messages, [{ seq: 7, from: 'agent', text: 'earlier answer' }]);
    assert.deepEqual([h.with_person, h.person, h.ended, h.contact_saved], [false, null, false, false]);
    assert.deepEqual((await g.call('/chat/history?visitor_id=someone_else_123456', { method: 'GET' })).data, { messages: [] });
  } finally { await g.close(); }
});

test('the email form saves a lead for a person, with no model run', async () => {
  const g = await start();
  try {
    assert.equal((await g.call('/chat/contact', { body: { visitor_id: VISITOR, email: 'a@b.co' } })).status, 404, 'needs a conversation first');
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } });
    const runs = g.calls.filter(([op]) => op === 'converse').length;
    for (const email of ['', 'no-at-sign', 'a@b', 'two@@example.com', 'x'.repeat(70) + '@example.com']) {
      assert.equal((await g.call('/chat/contact', { body: { visitor_id: VISITOR, email } })).status, 400, email);
    }
    const saved = await g.call('/chat/contact', { body: { visitor_id: VISITOR, email: 'pat@example.com', name: 'Pat', note: 'Booking emails', extra: 'dropped' } });
    assert.deepEqual(saved.data, { status: 'saved' });
    assert.equal(g.calls.filter(([op]) => op === 'converse').length, runs);
    const lead = JSON.parse((await readFile(join(g.dataDir, 'leads.jsonl'), 'utf8')).trim());
    assert.deepEqual([lead.email, lead.name, lead.need, lead.source, lead.extra], ['pat@example.com', 'Pat', 'Booking emails', 'form', undefined]);
    assert.equal(g.visitors.get(VISITOR).contact.email, 'pat@example.com');
    assert.ok(g.events.some(([type, data]) => type === 'lead.captured' && data.source === 'form'));
    const transcript = await g.call('/ops/transcript?session_id=sess-1', { method: 'GET', ops: true, key: OPS_KEY });
    assert.equal(transcript.data.contact.email, 'pat@example.com');
    // Each save alerts staff: a visitor gets three, then slow_down.
    for (const n of [2, 3]) assert.equal((await g.call('/chat/contact', { body: { visitor_id: VISITOR, email: `pat${n}@example.com` } })).status, 200);
    assert.equal((await g.call('/chat/contact', { body: { visitor_id: VISITOR, email: 'pat4@example.com' } })).status, 429);
    assert.equal(g.events.filter(([type]) => type === 'lead.captured').length, 3);
  } finally { await g.close(); }
});

test('phone alerts: only actionable events, in the alert service format, with a console link', async () => {
  const sent = [];
  const alert = core.createAlerter({ url: 'https://watch.example.com/', authHeader: 'Authorization: Bearer abc:def', consoleUrl: 'https://staff.example.com', fetchImpl: async (url, init) => { sent.push({ url, init }); return new Response('{}'); } });
  alert('conversation.started', { session_id: 's1' });
  alert('visitor.message', { session_id: 's1' });
  alert('handoff.requested', { session_id: 's1', summary: 'Clinic booking project', staff_online: true });
  alert('lead.captured', { session_id: 's1', lead: { email: 'pat@example.com', need: 'Quotes' } });
  await new Promise((r) => setImmediate(r));
  assert.equal(sent.length, 2, 'routine events are not alerts');
  assert.equal(sent[0].url, 'https://watch.example.com/v1/event');
  assert.equal(sent[0].init.headers.Authorization, 'Bearer abc:def');
  const handoff = JSON.parse(sent[0].init.body);
  assert.deepEqual([handoff.kind, handoff.severity, handoff.notify], ['chat.handoff', 'warning', true]);
  assert.match(handoff.body, /Clinic booking project/);
  assert.match(handoff.body, /Open: https:\/\/staff\.example\.com\/\?session=s1/);
  assert.match(JSON.parse(sent[1].init.body).body, /pat@example\.com/);
  assert.doesNotThrow(() => core.createAlerter({ url: '', authHeader: '' })('handoff.requested', {}), 'off when not configured');
});

test('a new visitor gets one conversation, the agent answers with its tools, and seq is returned', async () => {
  const g = await start();
  try {
    const first = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'How much?' } });
    assert.deepEqual(first, { status: 200, data: { status: 'answered', reply: 'Hello from the agent', seq: 9, person: null } });
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'And timing?' } });
    assert.equal(g.calls.filter(([op]) => op === 'create').length, 1, 'one session per visitor');
    assert.deepEqual(g.calls.find(([op]) => op === 'create')[1], { agent_name: 'frontdesk', workspace_path: '/project', title: 'Visitor visitor_' });
    assert.deepEqual(g.calls.find(([op]) => op === 'converse')[3], ['capture_lead', 'end_conversation', 'get_playbook', 'handoff_to_human', 'offer_person', 'propose_lead']);
    assert.equal(g.visitors.get(VISITOR).messages, 2);
    assert.deepEqual(g.events.map(([type]) => type), ['conversation.started']);
  } finally { await g.close(); }
});

test('during a takeover the message is kept for the person and no agent run is awaited', async () => {
  const g = await start({ takeover: { state: 'human', by: 'owner', since: 1 } });
  try {
    const res = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'Are you there?' } });
    assert.deepEqual(res, { status: 200, data: { status: 'with_person', seq: 7, person: null } });
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
  const tools = core.toolHandlers({ playbook: { pricing: 'Fixed price.' }, dataDir, bookingUrl: 'https://example.com/book', emit: (t, d) => events.push([t, d]), sessionId: 's1', visitorId: 'v1', presence: core.createPresence(), setPerson: async () => {}, now: () => 5 });
  assert.deepEqual(await tools.get_playbook({ topic: 'pricing' }), { topic: 'pricing', answer: 'Fixed price.' });
  await assert.rejects(tools.get_playbook({ topic: 'constructor' }));
  await tools.capture_lead({ name: 'Ada', email: 'ada@example.com', need: 'Quotes' });
  const file = join(dataDir, 'leads.jsonl');
  assert.deepEqual(JSON.parse(await readFile(file, 'utf8')), { at: 5, session_id: 's1', visitor_id: 'v1', source: 'agent', name: 'Ada', email: 'ada@example.com', need: 'Quotes' });
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

test("a person's replies carry the name the console sent, in the transcript, the polled replies and the history", async () => {
  const g = await start({ agentLabel: 'Ada (AI)' });
  try {
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'can I talk to someone?' } });
    await g.call('/ops/accept', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', name: 'Sam', role: 'founder' } });
    await g.call('/ops/reply', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', content: 'Hey, Sam here', name: 'Sam', role: 'founder' } });
    await g.call('/ops/reply', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', content: 'No name on this one' } });
    const t = (await g.call('/ops/transcript?session_id=sess-1', { method: 'GET', ops: true, key: OPS_KEY })).data;
    assert.equal(t.agent_label, 'Ada (AI)');
    const people = t.messages.filter((m) => m.from === 'person');
    assert.deepEqual(people.map((m) => m.author?.name), ['Sam', undefined]);
    const replies = (await g.call('/chat/replies?visitor_id=' + VISITOR + '&after=9', { method: 'GET' })).data.replies;
    assert.deepEqual(replies.map((r) => [r.text, r.name]), [['Hey, Sam here', 'Sam'], ['No name on this one', undefined]]);
    const history = (await g.call('/chat/history?visitor_id=' + VISITOR, { method: 'GET' })).data;
    assert.equal(history.messages.find((m) => m.text === 'Hey, Sam here').name, 'Sam');
    assert.equal((await g.call('/chat/status', { method: 'GET' })).data.agent_label, 'Ada (AI)');
  } finally { await g.close(); }
});

test('resolve closes the chat politely without a model run; reopen starts a fresh one', async () => {
  const g = await start();
  try {
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'thanks, that is all' } });
    const done = await g.call('/ops/resolve', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', name: 'Sam' } });
    assert.deepEqual(done.data, { ended: true, reason: 'resolved', network: false });
    assert.equal(g.visitors.get(VISITOR).ended.by, 'Sam');
    assert.ok(g.events.some(([type]) => type === 'conversation.resolved'));
    const before = g.calls.length;
    assert.deepEqual((await g.call('/chat', { body: { visitor_id: VISITOR, message: 'one more thing' } })).data, { status: 'ended', reason: 'resolved' });
    assert.equal(g.calls.length, before);
    assert.equal((await g.call('/chat/history?visitor_id=' + VISITOR, { method: 'GET' })).data.ended_reason, 'resolved');
    await g.call('/ops/reopen', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1' } });
    assert.equal((await g.call('/chat', { body: { visitor_id: VISITOR, message: 'one more thing' } })).data.status, 'answered');
  } finally { await g.close(); }
});

test("block closes the chat and refuses the visitor's network for every route until unblocked", async () => {
  const g = await start();
  const net = { 'X-Visitor-Net': 'net_abcdefghijkl' };
  const other = 'visitor_zyxwvutsrqponmlk';
  try {
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'abuse' }, headers: net });
    const blocked = await g.call('/ops/block', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', name: 'Sam' } });
    assert.deepEqual(blocked.data, { ended: true, reason: 'blocked', network: true });
    const before = g.calls.length;
    // A fresh browser on the same network gets nowhere, on any route that reaches staff or the model.
    assert.deepEqual((await g.call('/chat', { body: { visitor_id: other, message: 'hi again' }, headers: net })).data, { status: 'blocked' });
    assert.deepEqual((await g.call('/chat/person', { body: { visitor_id: other, accept: true }, headers: net })).data, { status: 'blocked' });
    assert.deepEqual((await g.call('/chat/contact', { body: { visitor_id: other, email: 'a@b.co' }, headers: net })).data, { status: 'blocked' });
    assert.equal(g.calls.length, before, 'no daemon call for a blocked network');
    // A different network is unaffected; a header from the ops side is ignored.
    assert.equal((await g.call('/chat', { body: { visitor_id: other, message: 'hi' }, headers: { 'X-Visitor-Net': 'net_somewhereelse' } })).data.status, 'answered');
    const list = (await g.call('/ops/blocks', { method: 'GET', ops: true, key: OPS_KEY })).data.blocks;
    assert.equal(list.length, 1);
    assert.equal(list[0].net, 'net_abcdefghijkl');
    assert.equal(list[0].by, 'Sam');
    assert.ok(list[0].until > Date.now() + 29 * 86_400_000);
    assert.equal((await g.call('/ops/transcript?session_id=sess-1', { method: 'GET', ops: true, key: OPS_KEY })).data.blocked, true);
    assert.equal((await g.call('/ops/unblock', { ops: true, key: OPS_KEY, body: { net: 'bad net!' } })).status, 400);
    assert.deepEqual((await g.call('/ops/unblock', { ops: true, key: OPS_KEY, body: { net: 'net_abcdefghijkl' } })).data, { unblocked: true });
    assert.equal((await g.call('/chat', { body: { visitor_id: 'visitor_newbrowser000000', message: 'hi' }, headers: net })).data.status, 'answered');
  } finally { await g.close(); }
});

test('a blocked network lapses after its days', async () => {
  let t = Date.now();
  const list = core.blocklist(new Map(), undefined, { now: () => t });
  await list.add('net_abcdefghijkl', { until: t + 1000 });
  assert.equal(list.blocked('net_abcdefghijkl'), true);
  t += 1001;
  assert.equal(list.blocked('net_abcdefghijkl'), false);
  assert.deepEqual(list.list(), []);
});

test('the agent can only fill in the lead card; the visitor sends it, which saves the lead and closes the chat', async () => {
  const card = { name: 'Dana', email: 'dana@firm.example', need: 'Moves client intake details into the case system by hand, about an hour a day.' };
  const g = await start({ proposeWith: card });
  try {
    const first = await g.call('/chat', { body: { visitor_id: VISITOR, message: 'we copy intake forms into our case system' } });
    assert.deepEqual(first.data.proposal, card);
    assert.equal(await readFile(join(g.dataDir, 'leads.jsonl'), 'utf8').catch(() => ''), '', 'nothing is saved before the visitor sends it');
    // "Not now" removes the card and the chat carries on.
    assert.deepEqual((await g.call('/chat/proposal', { body: { visitor_id: VISITOR, accept: false } })).data, { status: 'declined' });
    assert.equal(g.visitors.get(VISITOR).proposal, null);
    assert.equal(g.visitors.get(VISITOR).ended, undefined);
    // Filled in again and sent (with the visitor's edits): saved, then closed.
    await g.call('/chat', { body: { visitor_id: VISITOR, message: "ok, let's do it" } });
    const sent = await g.call('/chat/contact', { body: { visitor_id: VISITOR, name: 'Dana K', email: card.email, note: card.need, from_proposal: true } });
    assert.deepEqual(sent.data, { status: 'saved', ended: true });
    const lead = JSON.parse((await readFile(join(g.dataDir, 'leads.jsonl'), 'utf8')).trim().split('\n').pop());
    assert.equal(lead.source, 'proposal');
    assert.equal(lead.name, 'Dana K');
    assert.equal(g.visitors.get(VISITOR).ended.reason, 'lead_saved');
    assert.deepEqual((await g.call('/chat', { body: { visitor_id: VISITOR, message: 'thanks' } })).data, { status: 'ended', reason: 'lead_saved' });
    // The ordinary form (no card) still saves without closing anything.
    const g2 = await start();
    try {
      await g2.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } });
      assert.deepEqual((await g2.call('/chat/contact', { body: { visitor_id: VISITOR, email: 'a@b.co', from_proposal: true } })).data, { status: 'saved' });
      assert.equal(g2.visitors.get(VISITOR).ended, undefined);
    } finally { await g2.close(); }
  } finally { await g.close(); }
});

test('typing is shown both ways only while a person is in the chat, and lapses on its own', async () => {
  let t = Date.now();
  const typing = core.createTyping({ ttlMs: 1000, now: () => t });
  assert.equal(typing.staff('s1', 'Sam'), true, 'a new mark');
  assert.equal(typing.staff('s1', 'Sam'), false, 'a refresh is not news');
  assert.equal(typing.staffOf('s1').name, 'Sam');
  t += 1001;
  assert.equal(typing.staffOf('s1'), undefined);
  assert.equal(typing.visitor('s1'), true);
  typing.clearVisitor('s1');
  assert.equal(typing.visitorTyping('s1'), false);

  const g = await start();
  try {
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'hi' } });
    // No person involved yet: a visitor typing mark is ignored.
    assert.deepEqual((await g.call('/chat/typing', { body: { visitor_id: VISITOR } })).data, { ok: false });
    await g.call('/ops/accept', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', name: 'Sam', role: 'founder' } });
    assert.deepEqual((await g.call('/chat/typing', { body: { visitor_id: VISITOR } })).data, { ok: true });
    assert.equal((await g.call('/ops/transcript?session_id=sess-1', { method: 'GET', ops: true, key: OPS_KEY })).data.visitor_typing, true);
    assert.equal((await g.call('/ops/typing', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1' } })).status, 400, 'a name is required');
  } finally { await g.close(); }
});

test('the replies request is held open and answers as soon as a person types or replies', async () => {
  const g = await start();
  try {
    await g.call('/chat', { body: { visitor_id: VISITOR, message: 'can I talk to someone?' } });
    await g.call('/ops/accept', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', name: 'Sam', role: 'founder' } });
    const q = (extra) => `/chat/replies?visitor_id=${VISITOR}&after=9&state=joined&wait=5${extra}`;
    // Nothing new: it waits; a typing mark from the console wakes it at once.
    const t0 = Date.now();
    const waiting = g.call(q(''), { method: 'GET' });
    setTimeout(() => g.call('/ops/typing', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', name: 'Sam' } }), 150);
    const typed = (await waiting).data;
    assert.deepEqual(typed.typing, { name: 'Sam' });
    assert.deepEqual(typed.replies, []);
    assert.ok(Date.now() - t0 < 2000, 'answered on the typing mark, not at the timeout');
    // Already showing "Sam is typing": the next reply wakes it, and typing clears.
    const next = g.call(q('&typing=Sam'), { method: 'GET' });
    setTimeout(() => g.call('/ops/reply', { ops: true, key: OPS_KEY, body: { session_id: 'sess-1', content: 'Hi, Sam here', name: 'Sam', role: 'founder' } }), 150);
    const replied = (await next).data;
    assert.deepEqual(replied.replies.map((r) => [r.text, r.name]), [['Hi, Sam here', 'Sam']]);
    assert.equal(replied.typing, null);
    // Without wait it answers at once, as before.
    assert.equal((await g.call(`/chat/replies?visitor_id=${VISITOR}&after=99`, { method: 'GET' })).status, 200);
  } finally { await g.close(); }
});
