import assert from 'node:assert/strict'
import test from 'node:test'
import { projectTaskFollowupPayload } from './project-task-followup'
import { mapBackendTask } from '../state/desktop-projects-state'

// Requirement: retries after remount/reload have identical payload identity,
// while a new request/revision/project/repair mode cannot reuse it. The runtime
// payload helper is the narrowest boundary; no browser content persistence.
test('follow-up payload identities survive reload and separate changed contracts', async () => {
  const first = await projectTaskFollowupPayload('project', 'task', 3, ' full request\n')
  assert.deepEqual(await projectTaskFollowupPayload('project', 'task', 3, ' full request\n'), first)
  assert.equal(first.feedback, ' full request\n')
  for (const args of [
    ['other', 'task', 3, ' full request\n', false],
    ['project', 'task', 4, ' full request\n', false],
    ['project', 'task', 3, 'changed', false],
    ['project', 'task', 3, ' full request\n', true],
  ] as const) assert.notEqual((await projectTaskFollowupPayload(args[0], args[1], args[2], args[3], args[4])).client_request_id, first.client_request_id)
  await assert.rejects(projectTaskFollowupPayload('project', 'task', 0, 'request'))
})

// Requirement: backend reload retains task association, original session history
// and integration receipts without displaying the old summary as new work.
// mapBackendTask is the narrow backend-derived projection layer.
test('reload projects active attempt without historical summary leakage', () => {
  const attempts = [{ id: 'old', session_id: 'old-session', role: 'coder', status: 'needs_review', summary: 'Old result' }, { id: 'new', session_id: 'new-session', role: 'swarm', status: 'in_progress', request: 'Follow up' }]
  const raw = { id: 'task', title: 'Task', status: 'in_progress', agent: 'swarm', session_id: 'new-session', active_attempt_id: 'new', attempts, integration: { state: 'conflict', error: 'Retained conflict' } }
  const task = mapBackendTask(raw)
  assert.equal(task.sessionId, 'new-session')
  assert.equal(task.activeAttemptId, 'new')
  assert.deepEqual(task.attempts, attempts)
  assert.equal(task.integration?.error, 'Retained conflict')
  assert.equal(task.handoffSummary, undefined)
  assert.match(mapBackendTask({ ...raw, status: 'needs_review' }).handoffSummary || '', /No ready summary/)
  assert.equal(mapBackendTask({ ...raw, status: 'needs_review', attempts: [...attempts.slice(0, 1), { ...attempts[1], summary: 'New result' }] }).handoffSummary, 'New result')
})
