import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmWorkspacesNamespace } from '../workspaces.js';
import type { SwarmTransport } from '../transport.js';

// Purpose: workspaces.create must set up exactly parent/name (guarded by the
// same expected path) and then register it, and refuse names that could leave
// the parent folder before any request. Boundary: SwarmWorkspacesNamespace; a
// transport fixture is the wire contract, not a live daemon.
test('workspaces.create sets up and registers one folder under the parent', async () => {
  const calls: Array<[string, string, unknown]> = [];
  const transport = { request: async (path: string, options: { method: string; body?: unknown }) => {
    calls.push([options.method, path, options.body]);
    return { data: { ok: true, repository: { state: 'ready' }, workspace: { workspace_path: '/project/bot', workspace_id: 'ws1' } } };
  } } as unknown as SwarmTransport;
  const ns = new SwarmWorkspacesNamespace(transport);
  assert.equal((await ns.create({ parent_path: '/project/', name: 'bot' })).workspace_id, 'ws1');
  assert.deepEqual(calls, [
    ['POST', '/v1/workspace/repository/setup', { path: '/project/bot', expected_resolved_path: '/project/bot' }],
    ['POST', '/v1/workspace/add', { path: '/project/bot', name: 'bot', make_current: false }],
  ]);
  for (const name of ['..', 'a/b', '.git', '', '-x']) {
    await assert.rejects(ns.create({ parent_path: '/project', name }), /Workspace name/);
  }
  await assert.rejects(ns.create({ parent_path: 'project', name: 'bot' }), /absolute/);
  assert.equal(calls.length, 2);
});
