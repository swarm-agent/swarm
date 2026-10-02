import assert from 'node:assert/strict'
import test from 'node:test'
import { mediaJobIdentity } from './media-job-identity'

// Purpose: OrchestrateView's durable job adapter must not orphan a selected
// client request when a server task arrives or canonicalizes its source ID.
// This pure adapter test is the narrowest proof of identity, including reopen
// without optimistic state and unrelated-task isolation (not provider success).
test('durable media jobs preserve request identity and exact selected source', () => {
  const jobs = [{ id: 'request', taskId: 'task', sourceId: 'exact-source', count: 1, title: 'Extend', status: 'queued' }]
  assert.deepEqual(mediaJobIdentity('task', 'canonical-source', jobs), { id: 'request', sourceId: 'exact-source' })
  assert.deepEqual(mediaJobIdentity('other', 'other-source', jobs), { id: 'other', sourceId: 'other-source' })
  assert.deepEqual(mediaJobIdentity('task', 'canonical-source', []), { id: 'task', sourceId: 'canonical-source' })
})
