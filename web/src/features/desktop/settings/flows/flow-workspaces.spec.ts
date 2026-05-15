import test from 'node:test'
import assert from 'node:assert/strict'

function workspaceWire(path: string, name: string) {
  return {
    path,
    workspace_name: name,
    directories: [],
    is_git_repo: true,
    topology_routes: [],
    sort_index: 0,
    added_at: 0,
    updated_at: 0,
    last_selected_at: 0,
    active: true,
    worktree_enabled: false,
  }
}

async function withFetchStub(run: (calls: Array<{ input: RequestInfo | URL; init?: RequestInit }>) => Promise<void>): Promise<void> {
  const calls: Array<{ input: RequestInfo | URL; init?: RequestInit }> = []
  const originalFetch = globalThis.fetch
  globalThis.fetch = (async (input: RequestInfo | URL, init?: RequestInit) => {
    calls.push({ input, init })
    const url = String(input)
    if (url === '/v1/workspace/list?limit=200') {
      return new Response(JSON.stringify({ ok: true, workspaces: [workspaceWire('/local/workspace', 'local-workspace')] }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    if (url === '/v1/swarm/managed-workspaces/inventory?target_swarm_id=remote-swarm&limit=200') {
      return new Response(JSON.stringify({
        ok: true,
        target: { swarm_id: 'remote-swarm', name: 'Remote', online: true },
        managed_home: '/remote/home',
        saved_workspaces: [workspaceWire('/remote/workspace', 'remote-workspace')],
        discovered_directories: [],
        active_cwds: [],
      }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    }
    throw new Error(`unexpected fetch: ${url}`)
  }) as typeof fetch

  try {
    await run(calls)
  } finally {
    globalThis.fetch = originalFetch
  }
}

test('fetchFlowWorkspaces scopes workspace records to the selected target without mutation endpoints', async () => {
  const { fetchFlowWorkspaces } = await import('./api')

  await withFetchStub(async (calls) => {
    const local = await fetchFlowWorkspaces({ swarm_id: 'local-swarm', kind: 'self', name: 'Local', role: 'master', relationship: 'self', online: true, selectable: true, current: true })
    assert.deepEqual(local.map((workspace) => workspace.path), ['/local/workspace'])

    const remote = await fetchFlowWorkspaces({ swarm_id: 'remote-swarm', kind: 'remote', name: 'Remote', role: 'child', relationship: 'child', online: true, selectable: true, current: false })
    assert.deepEqual(remote.map((workspace) => workspace.path), ['/remote/workspace'])

    assert.deepEqual(calls.map((call) => String(call.input)), [
      '/v1/workspace/list?limit=200',
      '/v1/swarm/managed-workspaces/inventory?target_swarm_id=remote-swarm&limit=200',
    ])
    assert.ok(calls.every((call) => !call.init?.method || call.init.method === 'GET'))
  })
})
