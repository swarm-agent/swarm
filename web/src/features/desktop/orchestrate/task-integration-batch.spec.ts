import test from 'node:test'
import assert from 'node:assert/strict'
import { createTaskIntegrationBatchController, integrationSkipReason, MAX_INTEGRATION_BATCH } from './task-integration-batch'
import { acquireTaskMutation, createTaskIntegrationController, taskIntegrationKey } from './task-integration-operation'
import type { ProjectSummary, RunningTask } from './orchestrate-types'

const project = { id: 'batch-project', name: 'Project' } as ProjectSummary
const task = (id: string, extra: Partial<RunningTask> = {}): RunningTask => ({ id, title: id, status: 'needs_review', agentType: 'coder', workspaceTarget: 'local', elapsed: '', subtasks: [], sessionId: `session-${id}`, sourceWorkspacePath: '/fixture/repository', worktreeBranch: `agent/${id}`, baseBranch: 'dev', ...extra })
const receipt = (row: RunningTask) => ({ status: 'integrated', task: { id: row.id, session_id: row.sessionId!, is_integrated: true } })

// Purpose: the batch controller and shared integration authority must serialize
// selected writes, reject double-click/card races, retain exact receipts, and
// stop after conflict. Deferred transports are the narrowest deterministic test
// of ordering; no fixture performs Git operations or claims backend ancestry.
test('serial selected writes retain partial success and stop on published failure', { timeout: 5000 }, async () => {
  const batch = createTaskIntegrationBatchController(), operations = createTaskIntegrationController()
  const rows = ['a', 'b', 'c'].map(id => task(id))
  const calls: string[] = []
  let finish!: () => void, refreshes = 0
  const run = () => batch.run(project, rows, id => rows.find(row => row.id === id), (row, token) => operations.run(project, row, async () => {
    calls.push(row.id)
    if (row.id === 'a') await new Promise<void>(resolve => { finish = resolve })
    if (row.id === 'b') throw new Error('conflict')
    return receipt(row)
  }, () => {}, token), () => { refreshes++ })
  const pending = run()
  await run()
  assert.deepEqual(calls, ['a'])
  assert.equal(acquireTaskMutation(project.id, 'a'), undefined, 'reopen cannot race pending integration')
  assert.equal((await operations.run(project, rows[2], async () => { calls.push('unexpected'); return receipt(rows[2]) }, () => {})).status, 'skipped')
  finish(); await pending
  assert.deepEqual(calls, ['a', 'b'])
  assert.deepEqual(batch.getSnapshot().get(project.id)?.entries.map(entry => entry.status), ['integrated', 'failed', 'not_attempted'])
  assert.equal(operations.get(taskIntegrationKey(project.id, rows[1])).phase, 'error')
  assert.equal(refreshes, 1)
})

// Purpose: batch eligibility must never write media, active or unavailable tasks,
// while failed execution alone must not suppress canonically permitted recovery.
// Pure eligibility plus the real queue proves zero-write and bounded selection.
test('mixed and oversized selections explain skips without writes', { timeout: 5000 }, async () => {
  const batch = createTaskIntegrationBatchController()
  const rows = [task('media', { agentType: 'image' }), task('active', { status: 'running' }), task('missing', { sessionId: undefined }), task('done', { isIntegrated: true })]
  let writes = 0
  await batch.run(project, rows, id => rows.find(row => row.id === id), async () => { writes++; return { status: 'integrated' } }, () => {})
  assert.equal(writes, 0)
  assert.ok(batch.getSnapshot().get(project.id)?.entries.every(entry => entry.status === 'skipped' && entry.reason))
  assert.equal(integrationSkipReason(project.id, task('failed', { status: 'failed' })), undefined)
  await batch.run(project, Array.from({ length: MAX_INTEGRATION_BATCH + 1 }, (_, i) => task(String(i))), () => undefined, async () => { writes++; return { status: 'integrated' } }, () => {})
  assert.equal(writes, 0)
  assert.ok(batch.getSnapshot().get(project.id)?.entries.every(entry => entry.status === 'not_attempted'))
})

// Purpose: a queued task must still match its captured attempt/repository before
// dispatch; navigation/unmount is represented by unavailable current state. This
// runtime seam proves old callbacks cannot retarget a later task generation.
test('changed attempt and unavailable project stop queued writes', { timeout: 5000 }, async () => {
  for (const changed of [undefined, task('b', { activeAttemptId: 'new-attempt' }), task('b', { sourceWorkspaceGeneration: 2 })]) {
    const batch = createTaskIntegrationBatchController()
    const rows = [task('a'), task('b')], calls: string[] = []
    await batch.run(project, rows, id => id === 'a' ? rows[0] : changed, async row => { calls.push(row.id); return { status: 'integrated' } }, () => {})
    assert.deepEqual(calls, ['a'])
    assert.equal(batch.getSnapshot().get(project.id)?.entries[1].status, 'not_attempted')
  }
})

// Purpose: only an exact task/session/is_integrated receipt can establish success.
// Exercise the production operation, not Promise fulfillment, so internally
// published errors and malformed acknowledgements cannot inflate batch success.
test('invalid receipts and thrown failures are failures, not fulfilled success', { timeout: 5000 }, async () => {
  for (const result of [{ status: 'integrated' }, { status: 'integrated', task: { id: 'other', session_id: 'wrong', is_integrated: true } }]) {
    const operations = createTaskIntegrationController(), row = task('receipt')
    const outcome = await operations.run(project, row, async () => result, () => {})
    assert.equal(outcome.status, 'failed')
    assert.equal(operations.get(taskIntegrationKey(project.id, row)).phase, 'error')
  }
  const batch = createTaskIntegrationBatchController(), rows = [task('a'), task('b')]
  await batch.run(project, rows, id => rows.find(row => row.id === id), async () => { throw new Error('uncertain transport') }, () => {})
  assert.deepEqual(batch.getSnapshot().get(project.id)?.entries.map(entry => entry.status), ['failed', 'not_attempted'])
})
