import test from 'node:test'
import assert from 'node:assert/strict'
import { createTaskReopenController } from './task-reopen-operation'
import { createTaskIntegrationController } from './task-integration-operation'
import type { RunningTask } from './orchestrate-types'

const task: RunningTask = { id: 'task', revision: 9, title: 'Task', status: 'failed', agentType: 'coder', outcomeType: 'code_pr', subtasks: [], workspaceTarget: 'local', elapsed: '0s', sessionId: 'session', worktreeBranch: 'agent/task', baseBranch: 'dev' }
const receipt = { status: 'reopened', task: { id: 'task', session_id: 'followup', revision: 10 } }

// Purpose: the reopen controller owns interaction exclusion and retained retry
// identity, while durable task receipts own success. Unit injection is the narrowest
// layer proving negative authority cases cause neither requests nor applications.
test('reopen rejects absent authority and retains failed repair request exactly', { timeout: 5000 }, async () => {
  const controller = createTaskReopenController()
  const bodies: unknown[] = []
  let applied = 0
  const mutate = async (body: unknown) => { bodies.push(body); return receipt }
  const apply = () => { applied++ }
  assert.equal((await controller.run('project', 'task', undefined, 'instructions', mutate, apply)).ok, false)
  assert.equal(bodies.length, 0)
  const retained = { ...task, activeAttemptId: 'attempt', attempts: [{ id: 'attempt', session_id: 'followup', role: 'repair', status: 'failed', launch_state: 'launch_failed', client_request_id: 'retained-request', request_revision: 4, request: '  exact repair bytes  ', recovery: { session_id: 'origin' } }] }
  assert.equal((await controller.run('project', 'task', retained, 'different bytes', mutate, apply)).ok, false)
  assert.equal(bodies.length, 0)
  assert.equal(applied, 0)
  assert.equal((await controller.run('project', 'task', retained, '  exact repair bytes  ', mutate, apply)).ok, true)
  assert.deepEqual(bodies, [{ client_request_id: 'retained-request', revision: 4, feedback: '  exact repair bytes  ', repair: true }])
  assert.equal(applied, 1)
})

// Purpose: both directions of the task mutation race must be excluded even with
// multiple controller instances/remounts. Production acquireTaskMutation is the
// narrowest boundary; canonical in-progress receipts also exclude reopening.
test('integration blocks reopen and invalid response never applies task authority', { timeout: 5000 }, async () => {
  const controller = createTaskReopenController()
  const integration = createTaskIntegrationController()
  let finish!: (value: any) => void
  const flight = integration.run({ id: 'project', name: 'Project' }, task, () => new Promise(resolve => { finish = resolve }), () => {})
  let calls = 0
  let applied = 0
  assert.equal((await controller.run('project', 'task', task, 'instructions', async () => { calls++; return receipt }, () => { applied++ })).ok, false)
  assert.equal(calls, 0)
  finish({ status: 'integrated', task: { id: task.id, session_id: task.sessionId, is_integrated: true } })
  await flight
  const retained = { ...task, activeAttemptId: 'retry', attempts: [{ id: 'retry', role: 'followup', status: 'failed', session_id: 'followup', launch_state: 'launch_failed', request: 'instructions', request_revision: 3, client_request_id: 'retry' }] }
  for (const invalid of [{ status: 'reopened' }, { status: 'reopened', task: { ...receipt.task, id: 'wrong-task' } }, { status: 'unknown', task: receipt.task }]) {
    assert.equal((await controller.run('project', 'task', retained, 'instructions', async () => invalid, () => { applied++ })).ok, false)
  }
  assert.equal(applied, 0)
})
