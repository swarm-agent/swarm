import assert from 'node:assert/strict'
import test from 'node:test'

import { mapWorkspaceEntry, mapWorkspaceResolution } from './workspace'

test('workspace DTO mapping preserves CP4 identity fields', () => {
  const workspace = mapWorkspaceEntry({
    path: '/workspaces/app',
    workspace_id: 'ws_123',
    workspace_generation: 2,
    state: 'active',
    definition_status: 'completed',
    definition: 'A backend service for durable session orchestration.',
    definition_attempt_count: 2,
    definition_generation: 4,
    definition_updated_at: 40,
    workspace_name: 'app',
    theme_id: 'crimson',
    directories: [],
    is_git_repo: true,
    sort_index: 1,
    added_at: 10,
    updated_at: 20,
    last_selected_at: 30,
    active: true,
  })

  assert.equal(workspace.workspaceId, 'ws_123')
  assert.equal(workspace.workspaceGeneration, 2)
  assert.equal(workspace.state, 'active')
  assert.equal(workspace.definitionStatus, 'completed')
  assert.equal(workspace.definition, 'A backend service for durable session orchestration.')
  assert.equal(workspace.definitionAttempts, 2)
  assert.equal(workspace.definitionGeneration, 4)
  assert.equal(workspace.definitionUpdatedAt, 40)

  const resolution = mapWorkspaceResolution({
    requested_path: '/requested/app',
    resolved_path: '/workspaces/app',
    workspace_id: 'ws_123',
    workspace_generation: 2,
    state: 'active',
    definition_status: 'failed',
    definition_error: 'model unavailable',
    definition_model_suggestion: 'Change the Router model in Settings and add this workspace again.',
    definition_attempt_count: 3,
    definition_generation: 5,
    definition_updated_at: 50,
    local_workspace_binding_id: 'wb_123',
    workspace_name: 'app',
    theme_id: 'crimson',
  })

  assert.equal(resolution.workspaceId, 'ws_123')
  assert.equal(resolution.workspaceGeneration, 2)
  assert.equal(resolution.state, 'active')
  assert.equal(resolution.localWorkspaceBindingId, 'wb_123')
  assert.equal(resolution.definitionStatus, 'failed')
  assert.equal(resolution.definitionError, 'model unavailable')
  assert.equal(resolution.definitionSuggestion, 'Change the Router model in Settings and add this workspace again.')
  assert.equal(resolution.definitionAttempts, 3)
})
