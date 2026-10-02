import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import { createTaskIntegrationController, taskIntegrationKey, type TaskIntegrationResult } from './task-integration-operation'
import type { ProjectSummary, RunningTask } from './orchestrate-types'

// Purpose: the task integration interaction must lock immediately, wait for a
// confirmed exact-task receipt, and retain failures/terminal receipts despite
// stale snapshots and navigation. The threat is duplicate Git mutations or a
// different lane's outcome appearing on the selected card. Controller tests are
// the narrowest layer proving lifecycle ordering; wiring checks are supplementary,
// not evidence of backend Git execution or rendered pixel/layout correctness.
const project = { id: 'project-a', name: 'Project A' } as ProjectSummary
const task = { id: 'task-a', title: 'Task A', sessionId: 'session-a', worktreeBranch: 'agent/a', baseBranch: 'dev', isIntegrated: false } as RunningTask
const success = (row = task): TaskIntegrationResult => ({ status: 'integrated', task: { id: row.id, session_id: row.sessionId!, is_integrated: true } })
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

test('slow mutation locks synchronously; duplicates, snapshots and remount do not unlock it', async () => {
  const controller = createTaskIntegrationController()
  const request = deferred<TaskIntegrationResult>()
  const key = taskIntegrationKey(project.id, task)
  const phases: string[] = []
  const unsubscribe = controller.subscribe(() => phases.push(controller.get(key).phase))
  let calls = 0
  const mutate = () => { calls++; return request.promise }
  const operation = controller.run(project, task, mutate, () => {})
  assert.equal(controller.get(key).phase, 'pending')
  await controller.run(project, task, mutate, () => {})
  controller.dismiss(key)
  controller.reopened(key)
  unsubscribe() // leaving the view does not own/cancel the operation
  const stale = { ...task, revision: 1, isIntegrated: true, unintegratedCommits: 0 }
  assert.equal(controller.get(taskIntegrationKey(project.id, stale)).phase, 'pending')
  request.resolve(success())
  await operation
  assert.equal(controller.get(key).phase, 'success')
  assert.equal(controller.get(taskIntegrationKey(project.id, { ...task, isIntegrated: false })).phase, 'success')
  await controller.run(project, task, mutate, () => {})
  assert.equal(calls, 1)
  assert.deepEqual(phases, ['pending'])
  assert.equal(task.isIntegrated, false, 'UI receipt must not mutate backend task authority')
})

test('failure persists with captured redacted recovery; explicit retry waits for success', async () => {
  const controller = createTaskIntegrationController()
  const key = taskIntegrationKey(project.id, task)
  const first = deferred<TaskIntegrationResult>()
  const operation = controller.run(project, task, () => first.promise, () => { throw new Error('must not refresh after failure') })
  first.reject(new Error('Conflict token=secret-value'))
  await operation
  const error = controller.get(key)
  assert.equal(error.phase, 'error')
  if (error.phase !== 'error') throw new Error('Expected error')
  assert.match(error.failure.error, /Conflict token=\[REDACTED\]/)
  assert.equal(error.failure.task.sessionId, task.sessionId)
  assert.equal(error.failure.projectId, project.id)
  assert.equal(controller.get(key), error)
  const retry = deferred<TaskIntegrationResult>()
  const attempt = controller.run(project, task, () => retry.promise, () => {})
  assert.equal(controller.get(key).phase, 'pending')
  retry.resolve(success())
  await attempt
  assert.equal(controller.get(key).phase, 'success')
})

// Purpose: the shared mutation/integration lock must reject a replacement lane
// while the original request is outstanding; exact-key receipts must remain
// isolated after release. Controller promises prove this without live Git.
test('pending outcomes stay scoped and prevent overlapping target switches', async () => {
  const controller = createTaskIntegrationController()
  const other = { ...task, sessionId: 'session-b', baseBranch: 'release' }
  const a = deferred<TaskIntegrationResult>()
  const b = deferred<TaskIntegrationResult>()
  const first = controller.run(project, task, () => a.promise, () => {})
  assert.equal(controller.get(taskIntegrationKey('project-b', task)).phase, 'ready')
  assert.equal(controller.get(taskIntegrationKey(project.id, { ...task, id: 'task-b' })).phase, 'ready')
  const second = controller.run(project, other, () => b.promise, () => {})
  assert.equal((await second).status, 'skipped')
  assert.equal(controller.get(taskIntegrationKey(project.id, other)).phase, 'ready')
  a.reject(new Error('Old lane conflict'))
  await first
  const retry = controller.run(project, other, () => b.promise, () => {})
  b.resolve(success(other))
  await retry
  assert.equal(controller.get(taskIntegrationKey(project.id, other)).phase, 'success')
  assert.equal(controller.get(taskIntegrationKey(project.id, task)).phase, 'error')
})

test('missing lineage or unconfirmed/mismatched mutation result cannot report success', async () => {
  for (const result of [{ status: 'in_progress' }, success({ ...task, id: 'wrong-task' }), success({ ...task, sessionId: 'wrong-session' })]) {
    const controller = createTaskIntegrationController()
    let refreshed = false
    await controller.run(project, task, async () => result, () => { refreshed = true })
    assert.equal(controller.get(taskIntegrationKey(project.id, task)).phase, 'error')
    assert.equal(refreshed, false)
  }
  const controller = createTaskIntegrationController()
  const missing = { ...task, baseBranch: undefined }
  let called = false
  await controller.run(project, missing, async () => { called = true; return success() }, () => {})
  assert.equal(called, false)
  assert.equal(controller.get(taskIntegrationKey(project.id, missing)).phase, 'error')
})

test('refresh failure is not integration failure and cannot overwrite a reopened operation', async () => {
  const controller = createTaskIntegrationController()
  const key = taskIntegrationKey(project.id, task)
  await controller.run(project, task, async () => success(), () => { throw new Error('refresh failed') })
  const receipt = controller.get(key)
  assert.equal(receipt.phase, 'success')
  if (receipt.phase !== 'success') throw new Error('Expected success')
  assert.match(receipt.refreshError!, /Integration completed/)
  controller.reopened(key)
  const oldRefresh = deferred<void>()
  const old = controller.run(project, task, async () => success(), () => oldRefresh.promise)
  await Promise.resolve()
  assert.equal(controller.get(key).phase, 'success')
  controller.reopened(key)
  const next = deferred<TaskIntegrationResult>()
  const current = controller.run(project, task, () => next.promise, () => {})
  oldRefresh.reject(new Error('late refresh error'))
  await old
  assert.equal(controller.get(key).phase, 'pending')
  next.resolve(success())
  await current
  assert.equal(controller.get(key).phase, 'success')
})

test('all task card consumers use the controller and send exact lineage without a branch fallback', () => {
  const source = readFileSync(new URL('./OrchestrateView.tsx', import.meta.url), 'utf8')
  const handler = source.slice(source.indexOf('const handleIntegrateTask ='), source.indexOf('// Refine task with router'))
  assert.match(handler, /tasks\.find\(row => row\.id === taskId\)/)
  assert.match(handler, /session_id: task\.sessionId, source_branch: task\.worktreeBranch, target_branch: task\.baseBranch/)
  assert.match(handler, /taskIntegrationOperations\.run/)
  assert.equal((source.match(/integrationOperation=\{integrationForTask\(/g) || []).length, 5)
  assert.doesNotMatch(source, /integratingTaskFlights|integratingTaskIds|setIntegrationFailures/)
})
