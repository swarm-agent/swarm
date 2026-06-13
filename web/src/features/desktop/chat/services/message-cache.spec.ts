import test from 'node:test'
import assert from 'node:assert/strict'

import type { ChatMessageRecord } from '../types/chat'
import {
  appendPendingUserMessage,
  createPendingUserMessage,
  dedupeAndTrimMessages,
  isPendingUserMessage,
  mergeMessageIntoCache,
  removePendingUserMessage,
} from './message-cache'

function message(input: Partial<ChatMessageRecord> & Pick<ChatMessageRecord, 'id' | 'sessionId' | 'globalSeq' | 'role' | 'content'>): ChatMessageRecord {
  return {
    createdAt: input.createdAt ?? input.globalSeq,
    ...input,
  }
}

test('appendPendingUserMessage keeps optimistic send in the session message query cache', () => {
  const pending = createPendingUserMessage('session-a', 'hello', 4)

  const messages = appendPendingUserMessage([
    message({ id: 'msg_1', sessionId: 'session-a', globalSeq: 1, role: 'assistant', content: 'ready' }),
  ], pending)

  assert.equal(messages.length, 2)
  assert.equal(messages[1]?.id, 'pending-user:session-a:5')
  assert.equal(messages[1]?.sessionId, 'session-a')
  assert.equal(isPendingUserMessage(messages[1]!), true)
})

test('mergeMessageIntoCache replaces matching pending user message with authoritative server message', () => {
  const pending = createPendingUserMessage('session-a', 'send this', 10)
  const authoritative = message({
    id: 'msg_00011',
    sessionId: 'session-a',
    globalSeq: 11,
    role: 'user',
    content: 'send this',
    createdAt: 1234,
  })

  const messages = mergeMessageIntoCache([pending], authoritative)

  assert.deepEqual(messages, [authoritative])
})

test('mergeMessageIntoCache matches pending and canonical user messages by client request id', () => {
  const pending = createPendingUserMessage('session-a', 'same text', 3)
  const authoritative = message({
    id: 'msg_00004',
    sessionId: 'session-a',
    globalSeq: 4,
    role: 'user',
    content: 'edited text',
    createdAt: 1234,
    metadata: { client_request_id: pending.metadata?.client_request_id },
  })

  assert.deepEqual(mergeMessageIntoCache([pending], authoritative), [authoritative])
})

test('mergeMessageIntoCache isolates identical pending messages by session id', () => {
  const pending = createPendingUserMessage('session-a', 'same text', 3)
  const authoritative = message({
    id: 'msg_00004',
    sessionId: 'session-b',
    globalSeq: 4,
    role: 'user',
    content: 'same text',
    createdAt: 1234,
  })

  const messages = mergeMessageIntoCache([pending], authoritative)

  assert.equal(messages.length, 2)
  assert.equal(messages.some((entry) => entry.sessionId === 'session-a' && isPendingUserMessage(entry)), true)
  assert.equal(messages.some((entry) => entry.sessionId === 'session-b' && !isPendingUserMessage(entry)), true)
})

test('mergeMessageIntoCache preserves live tool messages when a canonical message reuses their sequence', () => {
  const liveTool = message({
    id: 'live-tool:call_bash_1',
    sessionId: 'session-a',
    globalSeq: 12,
    role: 'tool',
    content: '{"path_id":"run.tool-history.v2","tool":"bash","call_id":"call_bash_1","arguments":"{\\"command\\":\\"./scripts/check.sh\\"}"}',
    createdAt: 1200,
    toolMessage: {
      pathId: 'run.tool-history.v2',
      tool: 'bash',
      callId: 'call_bash_1',
      target: '',
      argumentsText: '{"command":"./scripts/check.sh"}',
      argumentsJson: { command: './scripts/check.sh' },
      output: '',
      completedOutput: '',
      error: '',
      durationMs: 0,
      summary: 'bash ./scripts/check.sh',
      state: 'running',
      editDiff: null,
      searchData: null,
      previewLines: ['$ ./scripts/check.sh'],
      taskRows: [],
    },
  })
  const assistant = message({
    id: 'msg_00012',
    sessionId: 'session-a',
    globalSeq: 12,
    role: 'assistant',
    content: 'working on it',
    createdAt: 1300,
  })

  const messages = mergeMessageIntoCache([liveTool], assistant)

  assert.equal(messages.length, 2)
  assert.equal(messages.some((entry) => entry.id === liveTool.id && entry.toolMessage?.tool === 'bash'), true)
  assert.equal(messages.some((entry) => entry.id === assistant.id && entry.role === 'assistant'), true)
})

test('mergeMessageIntoCache replaces canonical tool message updates by id', () => {
  const started = message({
    id: 'msg_tool_1',
    sessionId: 'session-a',
    globalSeq: 20,
    role: 'tool',
    content: 'started',
    createdAt: 2000,
  })
  const completed = message({
    id: 'msg_tool_1',
    sessionId: 'session-a',
    globalSeq: 20,
    role: 'tool',
    content: 'completed',
    createdAt: 2100,
  })

  const messages = mergeMessageIntoCache([started], completed)

  assert.deepEqual(messages, [completed])
})

test('dedupeAndTrimMessages keeps the newest 200 sorted messages after replacing duplicates', () => {
  const messages = Array.from({ length: 205 }, (_, index) => message({
    id: `msg_${index + 1}`,
    sessionId: 'session-a',
    globalSeq: index + 1,
    role: 'assistant',
    content: `message ${index + 1}`,
  }))
  const updatedDuplicate = message({
    id: 'msg_150',
    sessionId: 'session-a',
    globalSeq: 150,
    role: 'assistant',
    content: 'updated duplicate',
    createdAt: 9999,
  })

  const trimmed = dedupeAndTrimMessages([messages[10]!, ...messages, updatedDuplicate])

  assert.equal(trimmed.length, 200)
  assert.equal(trimmed[0]?.id, 'msg_6')
  assert.equal(trimmed[trimmed.length - 1]?.id, 'msg_205')
  assert.equal(trimmed.find((entry) => entry.id === 'msg_150')?.content, 'updated duplicate')
  assert.equal(trimmed.filter((entry) => entry.id === 'msg_150').length, 1)
  assert.deepEqual(trimmed.map((entry) => entry.globalSeq), [...trimmed.map((entry) => entry.globalSeq)].sort((left, right) => left - right))
})

test('mergeMessageIntoCache caps hot cache and preserves newer streamed messages', () => {
  const current = Array.from({ length: 200 }, (_, index) => message({
    id: `msg_${index + 1}`,
    sessionId: 'session-a',
    globalSeq: index + 1,
    role: 'assistant',
    content: `message ${index + 1}`,
  }))
  const streamed = message({ id: 'msg_201', sessionId: 'session-a', globalSeq: 201, role: 'assistant', content: 'streamed' })

  const trimmed = mergeMessageIntoCache(current, streamed)

  assert.equal(trimmed.length, 200)
  assert.equal(trimmed[0]?.id, 'msg_2')
  assert.equal(trimmed[trimmed.length - 1]?.id, 'msg_201')
})

test('removePendingUserMessage removes only the failed optimistic message', () => {
  const pending = createPendingUserMessage('session-a', 'failed', 7)
  const confirmed = message({ id: 'msg_1', sessionId: 'session-a', globalSeq: 1, role: 'user', content: 'kept' })

  assert.deepEqual(removePendingUserMessage([confirmed, pending], pending.id), [confirmed])
})
