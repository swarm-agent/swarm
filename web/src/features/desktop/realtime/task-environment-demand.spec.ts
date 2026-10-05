// Purpose: outgoing V3 worksets, not acceptFrame alone, must follow visible task
// source/attachment demand and eviction. The protocol builder + canonical cache
// reducer is the narrow layer proving selector identity and no transcript demand.
import test from 'node:test'
import assert from 'node:assert/strict'
import { buildTaskEnvironmentWorksets } from './v3-realtime-controller'
import { createEmptyDesktopV3CacheState } from '../state/desktop-v3-cache-reducer'
import { mapBackendTask, reduceDesktopProjectsState } from '../state/desktop-projects-state'

test('task environment demand follows dynamic attachments and releases on detach/eviction', () => {
  const state = createEmptyDesktopV3CacheState()
  state.projectsState = reduceDesktopProjectsState({}, { type: 'projects.beginLoad', projectId: 'p', requestId: 'r' })
  state.projectsState = reduceDesktopProjectsState(state.projectsState, { type: 'projects.environmentCatalog', projectId: 'p', workspaces: [{ workspaceId: 'w', path: '/projects/source' }, { workspaceId: 'env', path: '/projects/environment' }] })
  assert.deepEqual(buildTaskEnvironmentWorksets(state, 'client', 'account')[0].selector.workspace_paths, ['/projects/environment', '/projects/source'])
  state.projectsState.p.lastObservedAt = 1
  state.projectsState.p.tasks = [mapBackendTask({ id: 't', source_workspace: { workspace_id: 'w', path: '/projects/source' } })]
  const demand = () => buildTaskEnvironmentWorksets(state, 'client', 'account')
  assert.deepEqual(demand()[0].selector.workspace_paths, ['/projects/source'])
  state.projectsState.p.tasks[0].environmentAttachments = [{ id: 'a', revision: 1, account_scope_id: 'account', project_id: 'p', task_id: 't', environment_id: 'e', environment_name: 'Env', state: 'ready', expires_at: 1, source: { workspace_id: 'env' } }]
  assert.deepEqual(demand()[0].selector.workspace_paths, ['/projects/environment', '/projects/source'])
  assert.deepEqual(demand()[0].resources, ['projects'])
  assert.equal(demand()[0].auto_subscribe_sessions, false)
  state.projectsState.p.tasks[0].environmentAttachments![0].account_scope_id = 'foreign'
  assert.deepEqual(demand()[0].selector.workspace_paths, ['/projects/source'])
  state.projectsState.p.tasks[0].environmentAttachments = []
  assert.deepEqual(demand()[0].selector.workspace_paths, ['/projects/source'])
  state.projectsState = reduceDesktopProjectsState(state.projectsState, { type: 'projects.evict', projectId: 'p' })
  assert.deepEqual(demand(), [])
})
