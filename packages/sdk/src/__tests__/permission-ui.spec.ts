import assert from 'node:assert/strict';
import { test } from 'node:test';
import { answerPermission, describePermission, isAskUserPermission, SwarmPermissionsNamespace, PermissionResolutionError } from '../permissions.js';
import type { PendingPermissionRecord } from '../permissions.js';
import { SwarmTransport } from '../transport.js';

const permission = (args: unknown, tool = 'ask-user'): PendingPermissionRecord => ({
  id: 'p', session_id: 's', run_id: 'r', tool_name: tool, tool_arguments: JSON.stringify(args),
  status: 'pending', requirement: 'input', mode: 'auto', created_at: 1, updated_at: 1,
});
const choices = ['Yes', { label: 'No', value: 'no' }, { label: 'Custom response', value: '__custom__', allow_custom: true }];

// Requirement: render actual backend choices and serialize only human input, never invented answers.
// Threat: malformed JSON, missing answers, custom markers or duplicate IDs silently approve a question.
// Authority: describePermission/answerPermission; pure tests prove the smallest wire mapping boundary.
test('permission views and answers fail closed and preserve structured/custom input', () => {
  const single = permission({ question: 'Proceed?', options: choices });
  assert.equal(describePermission(single).kind, 'ask-user');
  assert.deepEqual(describePermission(single).actions, ['allow_once', 'deny']);
  assert.deepEqual(answerPermission(single, 'My own answer'), { reason: 'My own answer' });
  assert.throws(() => answerPermission(single, '__custom__'), /custom response/);
  assert.throws(() => answerPermission(single, ''), /Answer required/);
  const multi = permission({ questions: [
    { id: 'route', question: 'Route?', options: choices },
    { question: 'Next?', options: choices },
    { id: 'optional', question: 'Extra?', options: choices, required: false },
  ] });
  assert.deepEqual(JSON.parse(answerPermission(multi, { route: 'no', q_2: 'custom text' }).reason!), { answers: { route: 'no', q_2: 'custom text' } });
  assert.throws(() => answerPermission(multi, { route: 'Yes' }), /q_2/);
  assert.throws(() => answerPermission(multi, { unknown: 'Yes' }), /Unknown question/);
  assert.throws(() => answerPermission(multi, 'Yes'), /answers map/);
  for (const args of [null, [], { questions: [] }, { questions: [null] }, { question: 'Q', options: [null] }, { questions: [
    { id: 'x', question: 'Q', options: choices }, { id: 'x', question: 'Q', options: choices },
  ] }]) {
    const invalid = permission(args);
    assert.ok(describePermission(invalid).parseError);
    assert.deepEqual(describePermission(invalid).actions, ['deny']);
    assert.throws(() => answerPermission(invalid, 'Yes'));
  }
  assert.ok(describePermission({ ...single, tool_arguments: '{broken' }).parseError);
  assert.deepEqual(describePermission({ ...single, status: 'denied' }).actions, []);
  assert.throws(() => answerPermission({ ...single, status: 'denied' }, 'Yes'), /pending/);
  for (const alias of ['ASK_USER', 'ask-user', 'askuser', 'functions.ask-user', 'functions.askuser']) {
    assert.equal(isAskUserPermission(permission({}, alias)), true);
  }
  assert.equal(describePermission(permission({ command: 'echo hello' }, 'bash')).kind, 'tool');
});

// Requirement: null/invalid input never reaches transport; success must confirm identity and decision.
// Threat: stale allow vs deny, cancelled records, malformed envelopes, duplicate mutation and hidden failures.
// Authority: SwarmPermissionsNamespace.resolve/list + permission service resolveLocked idempotent return.
// Stub transport is narrower than a daemon and asserts the exact outgoing payload and call counts.
test('resolution validates inputs, conflicts and concurrent requests without swallowing errors', async () => {
  const transport = new SwarmTransport({ baseUrl: 'http://localhost' });
  let calls = 0;
  let response: any;
  let body: any;
  transport.request = async (_path: string, options: any) => { calls++; body = options?.body; return { data: response, status: 200 } as any; };
  const api = new SwarmPermissionsNamespace(transport);
  for (const id of [null, undefined, '', 1, {}]) {
    await assert.rejects(api.resolve(id as any, 'p', 'deny'), /sessionId/);
    await assert.rejects(api.resolve('s', id as any, 'deny'), /permissionId/);
    await assert.rejects(api.listSessionPending(id as any), /sessionId/);
  }
  for (const options of [null, { reason: 1 }, { approvedArguments: 'invalid' }, { approvedArguments: 'null' }, { approvedArguments: [] }]) {
    await assert.rejects(api.resolve('s', 'p', 'allow_once', options as any));
  }
  await assert.rejects(api.resolve('s', 'p', 'oops' as any), /action/);
  assert.equal(calls, 0);
  const p = permission({ question: 'Q', options: choices });
  response = { ok: true, session_id: 's', permission: { ...p, status: 'approved', decision: 'allow_once', reason: 'Yes', approved_arguments: '{"x":1}' } };
  assert.equal((await api.resolve('s', 'p', 'allow_once', { reason: 'Yes', approvedArguments: '{"x":1}' })).permission.id, 'p');
  assert.deepEqual(body, { action: 'allow_once', reason: 'Yes', approved_arguments: { x: 1 } });
  await assert.rejects(api.resolve('s', 'p', 'allow_once', { reason: 'Yes', approvedArguments: { x: 2 } }), /not confirmed/);
  for (const patch of [{ status: 'denied', decision: 'deny' }, { status: 'cancelled' }, { reason: 'No' }, { status: 'pending' }]) {
    response.permission = { ...p, status: 'approved', decision: 'allow_once', reason: 'Yes', ...patch };
    await assert.rejects(api.resolve('s', 'p', 'allow_once', { reason: 'Yes' }), e => e instanceof PermissionResolutionError && e.result === response);
  }
  for (const invalid of [null, { ok: false }, { ok: true, session_id: 'foreign', permission: p }, { ok: true, session_id: 's', permission: { ...p, id: 'other' } }]) {
    response = invalid;
    await assert.rejects(api.resolve('s', 'p', 'deny'), /Invalid permission/);
  }
  response = { ok: true, session_id: 's', permission: { ...p, status: 'denied', decision: 'deny_once' } };
  assert.equal((await api.resolve('s', 'p', 'deny')).permission.status, 'denied');
  for (const invalid of [null, { ok: false }, { ok: true, permissions: [null] }, { ok: true, permissions: [{ ...p, session_id: 'other' }] }]) {
    response = invalid; await assert.rejects(api.listSessionPending('s'), /Invalid permission list/);
  }
  response = { ok: true, count: 0, permissions: null };
  assert.deepEqual(await api.listSessionPending('s'), []);
  let finish!: (value: any) => void;
  transport.request = async () => { calls++; return new Promise(resolve => { finish = resolve; }); };
  const first = api.resolve('s', 'p', 'deny');
  const before = calls;
  await assert.rejects(api.resolve('s', 'p', 'allow_once'), /in flight/);
  assert.equal(calls, before);
  finish({ data: { ok: true, session_id: 's', permission: { ...p, status: 'denied', decision: 'deny' } } });
  await first;
  transport.request = async () => { throw new Error('backend unavailable'); };
  await assert.rejects(api.resolve('s', 'p', 'deny'), /backend unavailable/);
  await assert.rejects(api.resolve('s', 'p', 'deny'), /backend unavailable/);
});
