// Purpose: a Worker V2 proposal is accepted in the Workers sidebar, so the
// chat hides its manage_workers tool result (chat-markdown's tool view and
// the conversation pane's message filter both call
// isPendingWorkerProposalResult). The Orchestrator's manage_workers now
// answers in the catalog envelope {tool, action, machine, state, data, ...},
// with the proposal's status, review_kind and next_action under data.
// Threat: the check reading the envelope's top level (where they are absent)
// and every proposal reappearing as a chat tool card; or an older message,
// stored with the answer at the top level, losing the suppression; or other
// results being hidden. Boundary: workerToolAnswer and
// isPendingWorkerProposalResult, pure functions the chat uses; no runtime or
// network is needed.
import test from 'node:test'
import assert from 'node:assert/strict'
import { isPendingWorkerProposalResult, workerToolAnswer } from './worker-tool-answer'

const proposal = { status: 'pending_review', review_kind: 'worker_v2', next_action: 'await_worker_acceptance', worker_id: 'worker_1' }
const envelope = (action: string, data: unknown) =>
  ({ tool: 'manage_workers', action, machine: 'home', state: 'ok', data, next_action: 'The person accepts or denies the proposal in Swarm.', untrusted_content: true })

test('an enveloped manage_workers proposal is a pending worker proposal', () => {
  const out = envelope('propose', proposal)
  assert.equal(isPendingWorkerProposalResult(out), true)
  // The pane also reads the stored text.
  assert.equal(isPendingWorkerProposalResult(JSON.parse(JSON.stringify(out))), true)
  assert.deepEqual(workerToolAnswer(out), proposal)
})

test('an older top-level proposal is still recognised', () => {
  assert.equal(isPendingWorkerProposalResult(proposal), true)
  assert.deepEqual(workerToolAnswer(proposal), proposal)
})

test('other results are not hidden', () => {
  assert.equal(isPendingWorkerProposalResult(envelope('list', { workers: [] })), false)
  assert.equal(isPendingWorkerProposalResult(envelope('update', { status: 'updated', worker_id: 'worker_1' })), false)
  // The envelope's own next_action is not the proposal's.
  assert.equal(isPendingWorkerProposalResult({ ...envelope('propose', { status: 'pending_review' }), status: 'pending_review', review_kind: 'worker_v2', next_action: 'await_worker_acceptance' }), false)
  assert.equal(isPendingWorkerProposalResult(null), false)
  assert.equal(isPendingWorkerProposalResult('pending_review'), false)
  assert.equal(isPendingWorkerProposalResult([proposal]), false)
})
