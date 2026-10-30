// Purpose: the chat reads manage_projects results through one reader. Swarm
// answers every manage_projects call in the catalog envelope {tool, action,
// machine, state, data, next_action, next, untrusted_content}; messages
// stored before it hold the answer at the top level or a linked machine's
// {machine, action, data}. Threat: a task status or failure read from the
// envelope's top level (always absent), so failed or pending project calls
// show as "complete"; or an older message losing its answer. Boundary:
// manageProjectsAnswer/manageProjectsLinked and toolActivityPresentation, pure
// functions the chat tool view uses; no runtime or network is needed.
import test from 'node:test'
import assert from 'node:assert/strict'
import { catalogAnswerData, manageProjectsAnswer, manageProjectsLinked } from './manage-projects-answer'
import { toolActivityPresentation } from './tool-activity'

const envelope = (action: string, state: string, data: Record<string, unknown>, machine = 'home') =>
  ({ tool: 'manage_projects', action, machine, state, data, next_action: 'Tell the person.', untrusted_content: true })

test('the envelope is read through data', () => {
  const answer = manageProjectsAnswer(envelope('wait_tasks', 'timed_out', { tasks: [] }))
  assert.deepEqual(answer, { data: { tasks: [] }, state: 'timed_out', machine: 'home', nextAction: 'Tell the person.', envelope: true })
  assert.equal(manageProjectsLinked({ action: 'get_task' }, envelope('get_task', 'ok', {})), false)
  assert.equal(manageProjectsLinked({ action: 'get_task', machine: 'box' }, envelope('get_task', 'ok', {}, 'box')), true)
})

// manage_sessions answers in the same envelope: its session card reads data,
// and a message stored before the envelope keeps its answer.
test('a manage_sessions answer is read through data', () => {
  const sessions = { tool: 'manage_sessions', action: 'list', machine: 'home', state: 'ok', data: { items: [{ id: 's1' }], has_more: false }, untrusted_content: true }
  assert.deepEqual(catalogAnswerData(sessions), { items: [{ id: 's1' }], has_more: false })
  assert.deepEqual(catalogAnswerData({ action: 'list', items: [] }), { action: 'list', items: [] })
  assert.equal(catalogAnswerData(null), null)
})

test('older messages keep their answer', () => {
  assert.deepEqual(manageProjectsAnswer({ task: { id: 't1' } }).data, { task: { id: 't1' } })
  const linked = { machine: 'box', action: 'get_task', data: { task: { id: 'r1' } }, untrusted_content: true }
  assert.deepEqual(manageProjectsAnswer(linked).data, { task: { id: 'r1' } })
  assert.equal(manageProjectsLinked({ action: 'get_task' }, linked), true)
  assert.deepEqual(manageProjectsAnswer(null), { data: null, state: '', machine: '', nextAction: '', envelope: false })
})

test('a project call shows its task status from the envelope', () => {
  const failed = toolActivityPresentation('manage_projects', 'done', '', { action: 'get_task' }, envelope('get_task', 'ok', { task: { id: 't', status: 'failed' } }))
  assert.equal(failed.state, 'error')
  const pending = toolActivityPresentation('manage_projects', 'done', '', { action: 'propose_task' }, envelope('propose_task', 'ok', { task: { id: 't', status: 'pending_review' } }))
  assert.equal(pending.statusLabel, 'Pending')
  // An older message read the same way.
  assert.equal(toolActivityPresentation('manage_projects', 'done', '', { action: 'get_task' }, { task: { status: 'failed' } }).state, 'error')
})
