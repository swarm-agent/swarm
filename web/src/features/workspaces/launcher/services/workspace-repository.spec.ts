import assert from 'node:assert/strict'
import test from 'node:test'

import { mapWorkspaceRepositoryState, workspaceRepositorySetupPrompt } from './workspace-repository'

// Requirement: Desktop must preserve the backend's typed repository readiness
// result so only safe empty-directory setup is offered. The threat is collapsing
// assisted setup into an unsafe automatic action; this mapper is the narrowest layer.
test('repository readiness mapping preserves the backend setup contract', () => {
  assert.deepEqual(mapWorkspaceRepositoryState({
    state: 'not_repository',
    path: '/workspace/new',
    can_setup: true,
    needs_review: false,
    message: 'empty directory can be initialized safely',
  }), {
    state: 'not_repository',
    path: '/workspace/new',
    repositoryRoot: '',
    headCommit: '',
    canSetup: true,
    needsReview: false,
    message: 'empty directory can be initialized safely',
  })
})

// Requirement: non-empty and unborn repositories must enter a normal session
// with review-first guidance and explicit Git permission. The threat is silent
// staging or committing; this prompt contract is the narrowest deterministic proof.
test('assisted setup prompt requires review and permission before Git mutation', () => {
  const prompt = workspaceRepositorySetupPrompt({
    state: 'needs_assisted_setup',
    path: '/workspace/existing',
    repositoryRoot: '',
    headCommit: '',
    canSetup: false,
    needsReview: true,
    message: 'review existing files first',
  })
  assert.match(prompt, /review repository setup/i)
  assert.match(prompt, /ignore rules/i)
  assert.match(prompt, /Never silently stage or commit files/)
  assert.match(prompt, /explicit permission before every Git mutation/)
  assert.match(prompt, /HEAD resolves to a commit/)
})
