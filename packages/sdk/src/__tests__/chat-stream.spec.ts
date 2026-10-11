import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmChatNamespace, type ToolStreamItem } from '../chat.js';
import { SwarmTransport } from '../transport.js';
import type { SessionWatchState, V3Event, V3Message, V3RunState } from '../realtime.js';

function harness() {
  const chat = new SwarmChatNamespace(new SwarmTransport({ baseUrl: 'http://localhost' }));
  let update!: (state: SessionWatchState) => void;
  (chat as any).realtime.watchSession = (_id: string, options: any) => {
    update = options.onChange;
    return { ready: Promise.resolve(), done: new Promise(() => {}), dispose() {} };
  };
  const snapshot: any = { sessions_by_id: {}, events_by_session: { s: [] as V3Event[] }, current_run_state_by_session: {}, session_views_by_id: {} };
  const state: SessionWatchState = { sessionId: 's', snapshot, messages: [], live: [], status: 'live' };
  const run = (id: string, active: boolean, status = active ? 'running' : 'completed') => {
    snapshot.current_run_state_by_session.s = { session_id: 's', run_id: id, active, status, completed_at: active ? undefined : 1 } as V3RunState;
  };
  return { chat, state, snapshot, run, push: (patch: Partial<SessionWatchState>) => update(Object.assign(state, patch)) };
}
const event = (seq: number, event_type: string, payload: Record<string, unknown>): V3Event =>
  ({ id: `e${seq}`, session_id: 's', seq, event_type, ts_unix_ms: seq, payload });

// Requirement: concatenating onText deltas reproduces the assistant text exactly, across a
// reconnect whose fresh hydration is shorter than what was already shown (durable checkpoints
// lag live text) and when the turn commits while live text trails the final message.
// Threat: the reconnect state cleared the per-stream mark, so the next update re-sent the whole
// text as a "delta" (duplicated output); the committed tail was never reported. Boundary:
// chat.stream reducer with injected watch states; not provider-backed evidence.
test('onText deltas concatenate to the final text across reconnect and commit', async () => {
  const h = harness();
  const deltas: string[] = [];
  await h.chat.stream('s', { onText: d => deltas.push(d) });
  h.run('r1', true);
  h.push({ live: [{ runId: 'r1', streamId: 'st', text: 'Hello wor' }] });
  h.push({ status: 'reconnecting' });
  h.push({ status: 'syncing', live: [] });
  h.push({ status: 'live', live: [{ runId: 'r1', streamId: 'st', text: 'Hello' }] });
  h.push({ live: [{ runId: 'r1', streamId: 'st', text: 'Hello world' }] });
  h.push({ live: [] });
  h.run('r1', false);
  h.push({ messages: [{ id: 'm', session_id: 's', global_seq: 2, role: 'assistant', content: 'Hello world!', created_at: 1,
    metadata: { run_id: 'r1', stream_id: 'st' } } as V3Message] });
  assert.equal(deltas.join(''), 'Hello world!');
  assert.deepEqual(deltas, ['Hello wor', 'ld', '!']);
  // A committed message that does not extend the streamed text is never sent as a delta.
  h.run('r2', true);
  h.push({ live: [{ runId: 'r2', streamId: 'st2', text: 'abc' }] });
  h.run('r2', false);
  h.push({ live: [], messages: [...h.state.messages, { id: 'm2', session_id: 's', global_seq: 3, role: 'assistant', content: 'xyz', created_at: 1,
    metadata: { run_id: 'r2', stream_id: 'st2' } } as V3Message] });
  assert.deepEqual(deltas.slice(3), ['abc']);
});

// Requirement: reasoning, cancelled tools and run outcome are surfaced. Threat: onReasoning was
// wired only to live patches, which never carry reasoning, so it never fired; cancelled tools
// stayed "running" forever; onComplete could not tell a failed turn from a successful one.
// Hydrated history must not replay callbacks but must seed running tools and partial reasoning.
test('reasoning, tool lifecycle including cancellation, and run outcome reach callbacks once', async () => {
  const h = harness();
  const reasoning: string[] = [];
  const tools: string[] = [];
  const completed: [number, string][] = [];
  h.snapshot.events_by_session.s = [
    event(1, 'session.reasoning.delta', { run_id: 'r', reasoning_id: 'k', delta: 'Plan', delta_mode: 'replace' }),
    event(2, 'session.tool.started', { call_id: 'old', tool_name: 'read' }),
  ];
  h.run('r', true);
  await h.chat.stream('s', {
    onReasoning: (d, full) => reasoning.push(`${d}|${full}`),
    onToolStarted: (t: ToolStreamItem) => tools.push(`start:${t.id}`),
    onToolDelta: (t: ToolStreamItem, d) => tools.push(`delta:${t.id}:${d}`),
    onToolCompleted: (t: ToolStreamItem) => tools.push(`${t.status}:${t.id}:${t.output ?? ''}`),
    onComplete: (messages, run) => completed.push([messages.length, run.status]),
  });
  h.push({});
  assert.deepEqual(reasoning, []);
  assert.deepEqual(tools, []);
  h.snapshot.events_by_session.s = [...h.snapshot.events_by_session.s,
    event(3, 'session.reasoning.delta', { run_id: 'r', reasoning_id: 'k', delta: ' more', delta_mode: 'append' }),
    event(4, 'session.tool.delta', { call_id: 'old', output: 'chunk' }),
    event(5, 'session.tool.completed', { call_id: 'old', tool_name: 'read', output: 'done', status: 'completed' }),
    event(6, 'session.tool.started', { call_id: 'c2', tool_name: 'bash' }),
    event(7, 'session.tool.cancelled', { call_id: 'c2', tool_name: 'bash', status: 'cancelled' }),
    event(8, 'session.reasoning.delta', { run_id: 'r', reasoning_id: 'k', delta: 'Rewritten', delta_mode: 'replace' }),
  ];
  h.push({});
  h.push({});
  assert.deepEqual(reasoning, [' more|Plan more', 'Rewritten|Rewritten']);
  assert.deepEqual(tools, ['delta:old:chunk', 'completed:old:done', 'start:c2', 'cancelled:c2:']);
  h.run('r', false, 'failed');
  h.push({ messages: [{ id: 'f', session_id: 's', global_seq: 9, role: 'system', content: 'run failed', created_at: 1 } as V3Message] });
  h.push({});
  assert.deepEqual(completed, [[1, 'failed']]);
});

// Requirement: apps merge durable history tool cards (keyed by session.tool.* payload.call_id)
// with live chat tool cards, so every live tool object exposes `callId` equal to that durable
// call_id while `id` keeps its old value. Daemon payloads carry call_id plus
// tool_instance_id = `step-<n>:<call_id>` (sessionV3ProviderToolInstanceID in
// swarmd/internal/api/sessions_v3_executor.go); a payload with only the instance id must still
// resolve to the bare call id. Threat: live and history ids differ, so every tool rendered twice,
// and lifecycle events keyed by different id fields lost the running tool's arguments/output.
// Boundary: chat.stream reducer (the source for SwarmBrowserChat tool_* messages).
test('live tool objects expose the durable call_id as callId', async () => {
  const h = harness();
  const live: ToolStreamItem[] = [];
  const done: ToolStreamItem[] = [];
  h.run('r', true);
  await h.chat.stream('s', {
    onToolStarted: t => live.push({ ...t }),
    onToolDelta: t => live.push({ ...t }),
    onToolCompleted: t => done.push({ ...t }),
  });
  h.push({});
  h.snapshot.events_by_session.s = [
    // Exact daemon shape: both ids present.
    event(1, 'session.tool.started', { call_id: 'call_a', tool_instance_id: 'step-1:call_a', tool_name: 'read', arguments: '{"path":"a"}' }),
    event(2, 'session.tool.completed', { call_id: 'call_a', tool_instance_id: 'step-1:call_a', tool_name: 'read', output: 'A', status: 'completed' }),
    // Instance id only on start/delta, call_id on completion: one card, durable callId throughout.
    event(3, 'session.tool.started', { tool_instance_id: 'step-2:call_b', tool_name: 'bash', arguments: '{"command":"ls"}' }),
    event(4, 'session.tool.delta', { tool_instance_id: 'step-2:call_b', output: 'x' }),
    event(5, 'session.tool.completed', { call_id: 'call_b', tool_instance_id: 'step-2:call_b', tool_name: 'bash', status: 'completed' }),
  ];
  h.push({});
  assert.deepEqual(live.map(t => [t.id, t.callId]), [['call_a', 'call_a'], ['step-2:call_b', 'call_b'], ['step-2:call_b', 'call_b']]);
  assert.deepEqual(done.map(t => [t.id, t.callId, t.arguments, t.output]), [
    ['call_a', 'call_a', '{"path":"a"}', 'A'],
    ['step-2:call_b', 'call_b', '{"command":"ls"}', 'x'],
  ]);
  const historyIds = h.snapshot.events_by_session.s.filter((e: V3Event) => e.payload.call_id).map((e: V3Event) => e.payload.call_id);
  for (const t of done) assert.ok(historyIds.includes(t.callId), `${t.callId} matches a durable call_id`);
});
