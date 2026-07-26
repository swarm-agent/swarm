import test from 'node:test'
import assert from 'node:assert/strict'
import { buildMessageListCache, normalizeCommittedMessage } from './desktop-v3-cache-reducer'
import type { MessageSnapshot } from './desktop-v3-cache-types'

const toolEnvelope = JSON.stringify({
  path_id: 'run.tool-history.v2',
  tool: 'task',
  call_id: 'call-1',
  arguments: '{}',
  output: JSON.stringify({ launches: [] }),
})

function message(content = toolEnvelope): MessageSnapshot {
  return {
    id: 'message-1',
    session_id: 'session-1',
    global_seq: 1,
    role: 'tool',
    content,
    created_at: 1,
  }
}

test('committed tool envelopes are normalized at the message-list cache boundary', () => {
  const cache = buildMessageListCache([message()])
  assert.equal(cache.items[0]?.toolMessage?.tool, 'task')
  assert.deepEqual(cache.items[0]?.toolMessage?.taskRows, [])
})

test('already-normalized committed messages retain canonical tool object identity', () => {
  const first = normalizeCommittedMessage(message())
  const cache = buildMessageListCache([first])
  assert.equal(cache.items[0], first)
  assert.equal(cache.items[0]?.toolMessage, first.toolMessage)
})

test('non-tool committed content records a canonical null normalization', () => {
  const normalized = normalizeCommittedMessage(message('ordinary assistant text'))
  assert.equal(normalized.toolMessage, null)
})
