import test from 'node:test'
import assert from 'node:assert/strict'

test('standalone pending permission helper is not exported for Desktop V3', async () => {
  const queries = await import('./chat-queries')

  assert.equal('fetchSessionPendingPermissions' in queries, false)
})
