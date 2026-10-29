// Purpose: media the Orchestrator generates in a chat shows there as a card
// linked to its job. Requirement: a finished manage_media generate/retry call
// names its project media task through job_id media/PROJECT/TASK (and a
// manage_projects create_task for the image, video or sound agent through its
// task id); the card's state is generating until a file is ready, ready once
// one is, failed when the task ended without one. Threat: a card linked to the
// wrong job, a card for a failed call or a linked machine's job (not in this
// Swarm's project), or a failed job showing as generating forever.
// Boundary: manageMediaJobRef/manageMediaJobState, the pure functions the chat
// tool view uses; no runtime or network is needed to prove them.
import test from 'node:test'
import assert from 'node:assert/strict'
import { manageMediaJobRef, manageMediaJobState } from './manage-media-job'

function call(partial: Partial<Parameters<typeof manageMediaJobRef>[0]>): Parameters<typeof manageMediaJobRef>[0] {
  return { tool: 'manage_media', state: 'done', argumentsJson: null, argumentsText: '', outputJson: null, output: '', completedOutput: '', ...partial }
}

test('a finished media call names its job', () => {
  const generated = call({ argumentsText: JSON.stringify({ action: 'generate', kind: 'image', brief: 'a bee' }), output: JSON.stringify({ job_id: 'media/proj_1/task_m1', status: 'queued' }) })
  assert.deepEqual(manageMediaJobRef(generated), { projectId: 'proj_1', taskId: 'task_m1' })
  assert.deepEqual(manageMediaJobRef(call({ argumentsJson: { action: 'retry' }, outputJson: { job_id: 'media/p/t2' } })), { projectId: 'p', taskId: 't2' })
  const task = call({ tool: 'manage_projects', argumentsJson: { action: 'create_task', agent: 'video', project_id: 'p9' }, outputJson: { task: { id: 'task_v1' } } })
  assert.deepEqual(manageMediaJobRef(task), { projectId: 'p9', taskId: 'task_v1' })
})

test('other calls name no job', () => {
  const output = JSON.stringify({ job_id: 'media/p/t' })
  assert.equal(manageMediaJobRef(call({ state: 'running', argumentsJson: { action: 'generate' }, output })), null)
  assert.equal(manageMediaJobRef(call({ state: 'error', argumentsJson: { action: 'generate' }, output })), null)
  assert.equal(manageMediaJobRef(call({ argumentsJson: { action: 'get' }, output })), null)
  assert.equal(manageMediaJobRef(call({ argumentsJson: { action: 'generate' }, output: JSON.stringify({ job_id: 'worker/w/r' }) })), null)
  assert.equal(manageMediaJobRef(call({ argumentsJson: { action: 'generate' }, output: JSON.stringify({ job_id: 'media/p/t/x' }) })), null)
  assert.equal(manageMediaJobRef(call({ argumentsJson: { action: 'generate' }, outputJson: { machine: 'box', data: { job_id: 'media/p/t' } } })), null)
  assert.equal(manageMediaJobRef(call({ tool: 'manage_projects', argumentsJson: { action: 'create_task', agent: 'coder', project_id: 'p' }, outputJson: { task_id: 't' } })), null)
})

test('a job is generating, then ready or failed', () => {
  assert.equal(manageMediaJobState(null), 'generating')
  assert.equal(manageMediaJobState({ status: 'running', deliverables: [{ status: 'generating' }] }), 'generating')
  assert.equal(manageMediaJobState({ status: 'needs_review', deliverables: [{ status: 'failed' }, { status: 'ready' }] }), 'ready')
  assert.equal(manageMediaJobState({ status: 'failed', deliverables: [{ status: 'failed' }] }), 'failed')
  assert.equal(manageMediaJobState({ status: 'completed', deliverables: [] }), 'failed')
})
