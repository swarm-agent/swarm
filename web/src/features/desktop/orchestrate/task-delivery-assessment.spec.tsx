// Purpose: canonical mapBackendTask and task card projections must use the
// recorded task delta, never divergent-history counts or stale attempt success.
// Pure mapping/SSR and controller checks are the narrowest boundary proving
// truthful labels and rejection without invoking integration or mutating status.
import test from 'node:test'
import assert from 'node:assert/strict'
import { mapBackendTask } from '../state/desktop-projects-state'
import { taskCardFacts } from './task-card-summary'
import { taskDelivery, taskOutcome } from './task-outcome'
import { createTaskIntegrationController, taskIntegrationPhase } from './task-integration-operation'
import type { ProjectSummary } from './orchestrate-types'

const backend = {
  id: 'task', revision: 4, session_id: 'session', active_attempt_id: 'attempt', status: 'needs_review',
  worktree_branch: 'agent/task', base_branch: 'dev', base_commit: 'base', git_status: 'diverged',
  source_workspace: { workspace_id: 'workspace', workspace_generation: 1 },
  unintegrated_commits: 109,
  delivery_assessment: {
    task_id: 'task', task_revision: 4, session_id: 'session', attempt_id: 'attempt',
    workspace_id: 'workspace', workspace_generation: 1, base_oid: 'base', source_oid: 'source', target_oid: 'target',
    source_branch: 'agent/task', target_branch: 'dev', freshness: 'observed',
    state: 'history_rewritten', reason_code: 'base_not_on_target', reason: 'Recorded base missing from target',
    candidate_commits: 1, allowed_actions: [],
  },
}

test('rewritten history maps to review, not mass ready or integrated', async () => {
  const task = mapBackendTask(backend)
  assert.equal(task.status, 'needs_review')
  assert.equal(task.unintegratedCommits, 1)
  assert.equal(taskCardFacts(task).git, 'History rewritten — review task')
  assert.equal(taskDelivery(task)?.actionable, false)
  const staleSuccess = { ...task, isIntegrated: true, status: 'completed', gitStatus: 'clean' as const }
  assert.equal(taskIntegrationPhase(staleSuccess, { phase: 'success' }), 'ready')
  assert.match(taskOutcome(staleSuccess).delivery!, /not verified/)
  let calls = 0
  await createTaskIntegrationController().run({ id: 'project' } as ProjectSummary, task,
    async () => { calls++; return { status: 'ok' } }, () => { calls++ })
  assert.equal(calls, 0)
  assert.equal(task.status, 'needs_review')
})

test('current candidate delta is actionable, stale attempts and equivalent trees are not', () => {
  const task = mapBackendTask({ ...backend, delivery_assessment: { ...backend.delivery_assessment,
    state: 'candidate_work', allowed_actions: ['integrate'] } })
  assert.equal(taskDelivery(task)?.actionable, true)
  assert.equal(taskCardFacts(task).git, '1 task commit to integrate')
  assert.equal(taskDelivery({ ...task, activeAttemptId: 'replacement' })?.actionable, false)
  assert.equal(taskDelivery({ ...task, gitStatus: 'stale' })?.actionable, false)
  for (const state of ['history_equivalent', 'unavailable', 'empty']) {
    const other = { ...task, deliveryAssessment: { ...task.deliveryAssessment!, state }, isIntegrated: true }
    assert.equal(taskDelivery(other)?.integrated, false)
    assert.equal(taskDelivery(other)?.actionable, false)
  }
  const failed = { ...task, integration: { state: 'failed', error: 'Retained failure' } } as typeof task
  assert.equal(taskIntegrationPhase(failed), 'error')
  assert.equal(taskOutcome(failed).blocker?.message, 'Retained failure')
})
