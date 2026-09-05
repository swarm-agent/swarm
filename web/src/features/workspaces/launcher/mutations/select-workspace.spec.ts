import assert from 'node:assert/strict'
import test from 'node:test'
import { parseWorkspaceSelectResponse } from './select-workspace'
import { WorkspaceRepositoryPrerequisiteError } from '../services/workspace-repository'

// Requirement: selecting a saved workspace whose repository drifted must preserve
// the backend's structured remediation state. The threat is collapsing a typed
// prerequisite into a generic error that cannot reopen the safe setup UI. This
// response parser is the narrowest client boundary for the wire contract.
test('workspace selection preserves typed repository prerequisite failures', async () => {
  const response = new Response(JSON.stringify({
    ok: false,
    code: 'workspace_repository_not_ready',
    error: 'Swarm workspaces require an initial commit',
    repository: {
      state: 'needs_initial_commit',
      path: '/workspace',
      repository_root: '/workspace',
      can_setup: false,
      needs_review: true,
      message: 'Create the initial commit and retry',
    },
  }), { status: 409, headers: { 'Content-Type': 'application/json' } })

  await assert.rejects(
    parseWorkspaceSelectResponse(response),
    (error: unknown) => {
      assert.ok(error instanceof WorkspaceRepositoryPrerequisiteError)
      assert.equal(error.repository.state, 'needs_initial_commit')
      assert.equal(error.repository.path, '/workspace')
      assert.equal(error.repository.needsReview, true)
      return true
    },
  )
})
