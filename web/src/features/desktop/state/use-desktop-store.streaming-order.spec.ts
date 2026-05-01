import assert from 'node:assert/strict'
import test from 'node:test'

import type { ChatMessageRecord } from '../chat/types/chat'
import { mergeMessageIntoCache } from '../chat/services/message-cache'

function message(input: Partial<ChatMessageRecord> & Pick<ChatMessageRecord, 'id' | 'sessionId' | 'globalSeq' | 'role' | 'content'>): ChatMessageRecord {
  return {
    createdAt: input.createdAt ?? input.globalSeq,
    ...input,
  }
}

test('canonical stored messages keep assistant/tool/assistant order after streaming', () => {
  const messages = [
    message({ id: 'msg_1', sessionId: 'session-a', globalSeq: 1, role: 'assistant', content: 'preface before tool' }),
    message({ id: 'msg_2', sessionId: 'session-a', globalSeq: 2, role: 'tool', content: 'tool ok' }),
    message({ id: 'msg_3', sessionId: 'session-a', globalSeq: 3, role: 'assistant', content: 'final after tool' }),
  ].reduce<ChatMessageRecord[]>((current, next) => mergeMessageIntoCache(current, next), [])

  assert.deepEqual(messages.map((item) => [item.role, item.content]), [
    ['assistant', 'preface before tool'],
    ['tool', 'tool ok'],
    ['assistant', 'final after tool'],
  ])
})
