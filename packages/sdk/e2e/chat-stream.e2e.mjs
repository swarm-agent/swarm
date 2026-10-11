// SDK chat streaming end-to-end driver. Run through the Go harness, which hosts the real
// Swarm API server with a scripted provider (fixed test data, not a model):
//   (cd packages/sdk && npm run build)
//   (cd swarmd && SWARM_SDK_E2E=1 go test ./internal/api -run TestSDKChatStreamE2E -count=1 -v)
// It imports the built package exactly as an application would.
import assert from 'node:assert/strict';
import { SwarmClient, SwarmConflictError } from '../dist/index.js';

const baseUrl = required('SWARM_E2E_BASE_URL');
const workspace = required('SWARM_E2E_WORKSPACE');
const projectSessionId = required('SWARM_E2E_PROJECT_SESSION');
const client = new SwarmClient({ baseUrl, timeoutMs: 15_000 });
const log = (...args) => console.log(`[sdk-e2e ${new Date().toISOString().slice(11, 23)}]`, ...args);

function required(name) {
  const value = process.env[name];
  if (!value) throw new Error(`${name} is required`);
  return value;
}
async function control(path) {
  const res = await fetch(`${baseUrl}/__e2e/${path}`, { method: path === 'script' ? 'GET' : 'POST' });
  assert.equal(res.status, 200, `control ${path}`);
  return res.json();
}
async function until(what, predicate, timeoutMs = 15_000) {
  const deadline = Date.now() + timeoutMs;
  while (!predicate()) {
    if (Date.now() > deadline) throw new Error(`timed out waiting for ${what}`);
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
}

/** One chat.stream() watch with every callback recorded, and a handle on its live socket. */
async function watch(sessionId) {
  const rec = { deltas: [], fullTexts: [], completions: [], errors: [], statuses: [], sockets: [], state: undefined };
  const stream = await client.chat.stream(sessionId, {
    // The default factory, wrapped only to let this test sever the real WebSocket.
    socketFactory: async (path) => { const socket = await client.realtime.socket(path); rec.sockets.push(socket); return socket; },
    onState: (state) => { rec.state = state; if (rec.statuses.at(-1) !== state.status) rec.statuses.push(state.status); },
    onText: (delta, fullText) => { rec.deltas.push(delta); rec.fullTexts.push(fullText); },
    onComplete: (messages, run) => rec.completions.push({ messages, run }),
    onError: (error) => rec.errors.push(error.message),
  });
  await stream.ready;
  return { stream, rec, text: () => rec.deltas.join('') };
}
const lastSeq = (state, id) => state.snapshot.projections_by_session[id]?.last_event_seq;

// 1. Start a session and find it in the session list.
const script = await control('script');
const firstHalf = script.first.join('');
const expected = firstHalf + script.rest.join('');
const a = await client.chat.createSession({ title: 'SDK e2e A', workspace_path: workspace });
assert.ok(a.id, 'session created');
assert.ok((await client.sessions.list({ limit: 50 })).some((s) => s.id === a.id), 'new session is listed');
log('session A', 'created and listed');

// 2. Stream a turn, sever the socket mid-turn while durable checkpoints are held back,
//    and require the reconnected watch to finish the text exactly once.
const wa = await watch(a.id);
await client.chat.sendMessage(a.id, 'E2E_STREAM please');
await until('first half of live text', () => wa.text() === firstHalf);
assert.ok(wa.rec.deltas.length >= 1);
log('live text before drop', JSON.stringify(wa.text()), 'deltas', wa.rec.deltas.length);
wa.rec.sockets[0].terminate();
await until('reconnected watch', () => wa.rec.sockets.length === 2 && wa.rec.statuses.includes('reconnecting') && wa.rec.statuses.at(-1) === 'live');
await control('continue');
// Live output now starts beyond the durable prefix; nothing may be repeated meanwhile.
await new Promise((resolve) => setTimeout(resolve, 300));
assert.ok(expected.startsWith(wa.text()), `no repeated or out-of-order text while durable lags: ${JSON.stringify(wa.text())}`);
await control('release-durable');
await until('completion of streamed turn', () => wa.rec.completions.length === 1);
await new Promise((resolve) => setTimeout(resolve, 200));
assert.equal(wa.text(), expected, 'concatenated deltas equal the full assistant text');
for (let i = 1; i < wa.rec.fullTexts.length; i++) assert.ok(wa.rec.fullTexts[i].startsWith(wa.rec.fullTexts[i - 1]), 'fullText only grows');
const [done] = wa.rec.completions;
assert.equal(done.run.status, 'completed');
const committed = done.messages.filter((m) => m.role === 'assistant').at(-1);
assert.equal(committed?.content, expected, 'committed assistant message equals streamed text');
assert.equal(wa.rec.sockets.length, 2, 'exactly one reconnect: the gap did not trigger a reconnect loop');
assert.deepEqual(wa.rec.errors, []);
const events = wa.rec.state.snapshot.events_by_session[a.id];
assert.equal(new Set(events.map((e) => e.seq)).size, events.length, 'no duplicated durable events');
for (let i = 1; i < events.length; i++) assert.ok(events[i].seq > events[i - 1].seq, 'events ascend by seq');
log('streamed turn', { deltas: wa.rec.deltas.length, sockets: wa.rec.sockets.length, statuses: wa.rec.statuses });

// 3. Switch to a second session; the first watch is disposed and stays silent.
const b = await client.chat.createSession({ title: 'SDK e2e B', workspace_path: workspace });
const listed = (await client.sessions.list({ limit: 50 })).map((s) => s.id);
assert.ok(listed.includes(a.id) && listed.includes(b.id), 'both sessions listed');
wa.stream.dispose();
await wa.stream.done;
const aDeltas = wa.rec.deltas.length;
const wb = await watch(b.id);
await client.chat.sendMessage(b.id, 'E2E_SHORT');
await until('second session completion', () => wb.rec.completions.length === 1);
assert.equal(wb.text(), 'short reply');
assert.equal(wb.rec.completions[0].run.status, 'completed');
assert.equal(wa.rec.deltas.length, aDeltas, 'disposed watch received nothing');
log('switched to session B', JSON.stringify(wb.text()));

// 4. A failed turn is reported once with its status; the watch stays usable.
await client.chat.sendMessage(b.id, 'E2E_FAIL');
await until('failed turn', () => wb.rec.completions.length === 2);
assert.equal(wb.rec.completions[1].run.status, 'failed');
assert.ok(wb.rec.completions[1].messages.some((m) => m.role === 'system'), 'failure is visible as a system message');
assert.deepEqual(wb.rec.errors, []);
log('failed turn', wb.rec.completions[1].run.status, wb.rec.completions[1].run.blocked_reason ?? '');

// 5. Context clear: rejected for a standalone chat (no change), accepted for a project
//    conversation, and the clear arrives on that conversation's stream.
await assert.rejects(client.sessions.clearContext(b.id, { expectedLastEventSeq: lastSeq(wb.rec.state, b.id) }), SwarmConflictError);
wb.stream.dispose();
await wb.stream.done;
const wp = await watch(projectSessionId);
const before = lastSeq(wp.rec.state, projectSessionId);
const cleared = await client.sessions.clearContext(projectSessionId, { expectedLastEventSeq: before });
assert.equal(cleared.ok, true);
await until('context clear on the stream', () => (wp.rec.state.snapshot.events_by_session[projectSessionId] ?? [])
  .some((e) => e.seq > before && e.event_type === 'execution_epoch.began'));
await assert.rejects(client.sessions.clearContext(projectSessionId, { expectedLastEventSeq: before }), SwarmConflictError);
wp.stream.dispose();
await wp.stream.done;
log('context clear', 'standalone rejected, project conversation cleared and streamed');
log('PASS');
