import assert from 'node:assert/strict'
import test from 'node:test'
import { QueryClient } from '@tanstack/react-query'
import { refreshWorkspaceCatalog, publishWorkspaceCatalog } from './workspace-catalog-refresh'
import { workspaceOverviewQueryKey } from '../../../queries/query-options'
import { mapWorkspaceEntry } from '../types/workspace'
import type { WorkspaceOverviewResponse } from '../types/workspace-overview'

// Requirement: one shared event-driven catalog read, preserving identity changes
// without discarding separately hydrated details. Threat: burst amplification,
// stale in-flight resurrection, and cached rows surviving deletion. This layer
// exercises the coordinator and real QueryClient, not React source strings.
function overview(name: string): WorkspaceOverviewResponse {
 return { ok: true, currentWorkspace: null, discovered: [], swarmTarget: null,
  workspaces: [{ ...mapWorkspaceEntry({ path: '/workspace', workspace_name: name }), workspaceId: 'workspace-a', sessions: [] }] }
}

test('catalog publishes names and deletion while retaining independent Git details', () => {
 const client = new QueryClient()
 const previous = overview('Before')
 previous.workspaces[0].gitBranch = 'dev'
 client.setQueryData(workspaceOverviewQueryKey(), previous)
 publishWorkspaceCatalog(client, overview('After'))
 const next = client.getQueryData<WorkspaceOverviewResponse>(workspaceOverviewQueryKey())!
 assert.equal(next.workspaces[0].workspaceName, 'After')
 assert.equal(next.workspaces[0].gitBranch, 'dev')
 publishWorkspaceCatalog(client, { ...overview('empty'), workspaces: [] })
 assert.deepEqual(client.getQueryData<WorkspaceOverviewResponse>(workspaceOverviewQueryKey())!.workspaces, [])
 client.clear()
})

test('bursts share a request and in-flight changes cause one trailing read', async () => {
 const client = new QueryClient()
 let reads = 0
 let release!: () => void
 let entered!: () => void
 const started = new Promise<void>((resolve) => { entered = resolve })
 const waiting = new Promise<void>((resolve) => { release = resolve })
 client.fetchQuery = (async () => {
  reads++
  if (reads === 1) { entered(); await waiting }
  return overview(reads === 1 ? 'stale' : 'fresh')
 }) as typeof client.fetchQuery
 const first = refreshWorkspaceCatalog(client)
 for (let i = 0; i < 100; i++) assert.equal(refreshWorkspaceCatalog(client), first)
 await started
 for (let i = 0; i < 100; i++) refreshWorkspaceCatalog(client)
 release()
 await first
 assert.equal(reads, 2)
 assert.equal(client.getQueryData<WorkspaceOverviewResponse>(workspaceOverviewQueryKey())!.workspaces[0].workspaceName, 'fresh')
 client.clear()
})

test('failed refresh rejects and a later event recovers without a retry loop', async () => {
 const client = new QueryClient()
 let reads = 0
 client.fetchQuery = (async () => { reads++; throw new Error('offline') }) as typeof client.fetchQuery
 await assert.rejects(refreshWorkspaceCatalog(client), /offline/)
 assert.equal(reads, 1)
 client.fetchQuery = (async () => overview('recovered')) as typeof client.fetchQuery
 await refreshWorkspaceCatalog(client)
 assert.equal(client.getQueryData<WorkspaceOverviewResponse>(workspaceOverviewQueryKey())!.workspaces[0].workspaceName, 'recovered')
 client.clear()
})
