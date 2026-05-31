import assert from 'node:assert/strict'
import test from 'node:test'

import { mapWorkspaceEntry, mapWorkspaceResolution } from './workspace'

test('workspace DTO mapping preserves CP4 identity fields', () => {
  const workspace = mapWorkspaceEntry({
    path: '/workspaces/app',
    workspace_id: 'ws_123',
    workspace_generation: 2,
    state: 'active',
    workspace_name: 'app',
    theme_id: 'crimson',
    directories: [],
    is_git_repo: true,
    sort_index: 1,
    added_at: 10,
    updated_at: 20,
    last_selected_at: 30,
    active: true,
    worktree_enabled: false,
  })

  assert.equal(workspace.workspaceId, 'ws_123')
  assert.equal(workspace.workspaceGeneration, 2)
  assert.equal(workspace.state, 'active')

  const resolution = mapWorkspaceResolution({
    requested_path: '/requested/app',
    resolved_path: '/workspaces/app',
    workspace_id: 'ws_123',
    workspace_generation: 2,
    state: 'active',
    local_workspace_binding_id: 'wb_123',
    workspace_name: 'app',
    theme_id: 'crimson',
  })

  assert.equal(resolution.workspaceId, 'ws_123')
  assert.equal(resolution.workspaceGeneration, 2)
  assert.equal(resolution.state, 'active')
  assert.equal(resolution.localWorkspaceBindingId, 'wb_123')
})
