import assert from 'node:assert/strict'
import test from 'node:test'

import { waitForWorkspaceDefinition } from './workspace-definition-lifecycle'
import type { WorkspaceEntry, WorkspaceResolution } from '../types/workspace'

const pendingResolution: WorkspaceResolution = {
  requestedPath: '/workspace/app',
  resolvedPath: '/workspace/app',
  workspaceId: 'ws_app',
  workspaceGeneration: 1,
  state: 'active',
  localWorkspaceBindingId: 'wb_app',
  workspaceName: 'app',
  themeId: '',
  definitionStatus: 'pending',
  definition: '',
  definitionError: '',
  definitionSuggestion: '',
  definitionAttempts: 0,
  definitionGeneration: 2,
  definitionUpdatedAt: 10,
}

function workspace(status: 'pending' | 'completed' | 'failed'): WorkspaceEntry {
  return {
    path: '/workspace/app',
    workspaceId: 'ws_app',
    workspaceGeneration: 1,
    state: 'active',
    localWorkspaceBindingId: 'wb_app',
    workspaceName: 'app',
    themeId: '',
    directories: [],
    isGitRepo: true,
    sortIndex: 0,
    addedAt: 1,
    updatedAt: 2,
    lastSelectedAt: 0,
    active: true,
    topologyRoutes: [],
    definitionStatus: status,
    definition: status === 'completed' ? 'A durable session backend.' : '',
    definitionError: status === 'failed' ? 'router request failed' : '',
    definitionSuggestion: status === 'failed' ? 'Change the Router model in Settings and add this workspace again.' : '',
    definitionAttempts: status === 'failed' ? 3 : 1,
    definitionGeneration: 2,
    definitionUpdatedAt: 20,
  }
}

test('workspace definition polling waits for the canonical completed entry', async () => {
  const responses = [[workspace('pending')], [workspace('completed')]]
  let loads = 0
  let delays = 0
  const completed = await waitForWorkspaceDefinition(pendingResolution, {
    load: async () => responses[loads++] ?? responses.at(-1)!,
    delay: async () => { delays += 1 },
  })

  assert.equal(completed.definitionStatus, 'completed')
  assert.equal(completed.definition, 'A durable session backend.')
  assert.equal(loads, 2)
  assert.equal(delays, 2)
})

test('workspace definition polling exposes the durable error and Router model guidance', async () => {
  await assert.rejects(
    waitForWorkspaceDefinition(pendingResolution, {
      load: async () => [workspace('failed')],
      delay: async () => {},
    }),
    /router request failed Change the Router model in Settings/,
  )
})
