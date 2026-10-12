import assert from 'node:assert/strict';
import { test } from 'node:test';
import { SwarmConflictError, SwarmWorkspaceNotVisibleError } from '../errors.js';
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

// Purpose: a folder Swarm cannot see on this machine (409 workspace_not_visible)
// must reach the app as SwarmWorkspaceNotVisibleError carrying the host change
// to request and whether the machine can apply it, so the app asks the owner
// instead of retrying a dead path; every other refusal keeps its own type.
// Boundary: SwarmWorkspacesNamespace.add over the transport's typed errors.
test('workspaces.add surfaces workspace_not_visible with its host change', async () => {
  const body = { ok: false, code: 'workspace_not_visible', error: '/home/person/app is not visible to Swarm on this machine.', host_change: { op: 'mount-workspace', path: '/home/person/app' }, supported: true };
  const refuse = (payload: Record<string, unknown>, code?: string) =>
    ({ request: async () => { throw new SwarmConflictError(String(payload.error), { code, details: payload }); } }) as unknown as SwarmTransport;
  await assert.rejects(
    new SwarmWorkspacesNamespace(refuse(body, 'workspace_not_visible')).add({ path: '/home/person/app', context_only: true, require_existing_content: true }),
    (err: unknown) =>
      err instanceof SwarmWorkspaceNotVisibleError && err instanceof SwarmConflictError && err.status === 409 && err.code === 'workspace_not_visible' &&
      err.supported === true && err.hostChange.op === 'mount-workspace' && err.hostChange.path === '/home/person/app' && /not visible/.test(err.message),
  );
  await assert.rejects(
    new SwarmWorkspacesNamespace(refuse({ ...body, supported: false }, 'workspace_not_visible')).add({ path: '/home/person/app' }),
    (err: unknown) => err instanceof SwarmWorkspaceNotVisibleError && err.supported === false,
  );
  // Other conflicts, and a not_visible reply without its host change, keep their type.
  const other = { ok: false, code: 'workspace_repository_not_ready', error: 'not ready', repository: {} };
  await assert.rejects(
    new SwarmWorkspacesNamespace(refuse(other, 'workspace_repository_not_ready')).add({ path: '/home/person/app' }),
    (err: unknown) => err instanceof SwarmConflictError && !(err instanceof SwarmWorkspaceNotVisibleError) && err.code === 'workspace_repository_not_ready',
  );
  await assert.rejects(
    new SwarmWorkspacesNamespace(refuse({ ok: false, code: 'workspace_not_visible', error: 'x' }, 'workspace_not_visible')).add({ path: '/x' }),
    (err: unknown) => err instanceof SwarmConflictError && !(err instanceof SwarmWorkspaceNotVisibleError),
  );
});
