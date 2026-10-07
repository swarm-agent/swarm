// Purpose: useProjectWorkspaceGit's source matching and push refresh coalescer
// must not conflate nested repositories or drop invalidations during a read.
// These pure boundaries are the narrowest proof of identity and scheduling;
// the browser header test separately exercises queries and push subscription.
import test from 'node:test'
import assert from 'node:assert/strict'
import { coalescedGitRefresh, projectWorkspaces, taskMatchesWorkspace } from './use-project-workspace-git'
import type { RunningTask, ProjectSummary } from '../orchestrate/orchestrate-types'

const project = { workspaces: [
  { workspace_id: 'outer', path: '/fixture/repo', label: 'Core' },
  { workspace_id: 'nested', path: '/fixture/repo/nested' },
], linkedWorkspaces: [] } as unknown as ProjectSummary
const task = (fields: Partial<RunningTask>) => fields as RunningTask

test('catalog retains all names/identities and source links do not infer nested ownership', () => {
  const [outer, nested] = projectWorkspaces(project)
  assert.equal(outer.name, 'Core')
  assert.equal(nested.name, 'nested')
  assert.equal(taskMatchesWorkspace(task({ sourceWorkspaceId: 'outer', workspacePath: '/fixture/isolated-task' }), outer), true)
  assert.equal(taskMatchesWorkspace(task({ sourceWorkspaceId: 'nested', workspacePath: outer.path }), outer), false)
  assert.equal(taskMatchesWorkspace(task({ sourceWorkspacePath: nested.path, workspacePath: outer.path }), outer), false)
  assert.equal(taskMatchesWorkspace(task({ sourceWorkspacePath: nested.path }), nested), true)
  assert.equal(taskMatchesWorkspace(task({ workspacePath: outer.path }), outer), true)
  assert.equal(taskMatchesWorkspace(task({ workspacesInvolved: [nested.path] }), nested), true)
  assert.equal(taskMatchesWorkspace(task({ workspacePath: '/fixture/repo/nested' }), outer), false)
  assert.equal(taskMatchesWorkspace(task({ workspaceTarget: 'Core' }), outer), false)
  assert.deepEqual(projectWorkspaces(undefined), [])
})

test('push bursts queue one trailing read and disposal prevents delayed refresh', async () => {
  const resolves: Array<() => void> = []
  const lane = coalescedGitRefresh(() => new Promise<void>(resolve => resolves.push(resolve)))
  lane.request()
  for (let i = 0; i < 10; i++) lane.request()
  assert.equal(resolves.length, 1)
  resolves[0]()
  await Promise.resolve()
  assert.equal(resolves.length, 2)
  lane.request()
  lane.dispose()
  resolves[1]()
  await Promise.resolve()
  lane.request()
  assert.equal(resolves.length, 2)
})

test('failed reads do not retry themselves but the next notice may recover', async () => {
  let calls = 0
  const lane = coalescedGitRefresh(async () => { calls++; throw new Error('Unavailable') })
  lane.request()
  await Promise.resolve()
  await Promise.resolve()
  assert.equal(calls, 1)
  lane.request()
  await Promise.resolve()
  assert.equal(calls, 2)
  lane.dispose()
})
