import assert from 'node:assert/strict'
import test from 'node:test'
import { formatConversationMarkdown, loadCompleteConversationMessages, sanitizeTranscriptFilename } from './transcript-export'
import type { ChatMessageRecord } from '../types/chat'

const message = (id: string, globalSeq: number, role: string, content: string): ChatMessageRecord => ({ id, sessionId: 's1', globalSeq, role, content, createdAt: globalSeq })

test('formats chronological user and assistant messages while excluding internal roles', () => {
  const markdown = formatConversationMarkdown({ title: 'Example', workspaceName: 'Demo' }, [
    message('a', 3, 'assistant', 'Answer'),
    message('t', 2, 'tool', 'secret tool output'),
    message('u', 1, 'user', 'Question'),
    message('r', 4, 'reasoning', 'private reasoning'),
  ])
  assert.match(markdown, /^# Example/)
  assert.ok(markdown.indexOf('Question') < markdown.indexOf('Answer'))
  assert.doesNotMatch(markdown, /secret|private/)
})

test('sanitizes Markdown filenames', () => {
  assert.equal(sanitizeTranscriptFilename('  My / Great: Chat!  '), 'my-great-chat.md')
  assert.equal(sanitizeTranscriptFilename('***'), 'conversation.md')
})

test('loads and deduplicates every older page', async () => {
  const calls: number[] = []
  const result = await loadCompleteConversationMessages([message('c', 3, 'assistant', 'three')], async (beforeSeq) => {
    calls.push(beforeSeq)
    if (beforeSeq === 3) return { messages: [message('b', 2, 'user', 'two')], hasMoreOlder: true, nextBeforeSeq: 2 }
    return { messages: [message('a', 1, 'assistant', 'one')], hasMoreOlder: false, nextBeforeSeq: 1 }
  })
  assert.deepEqual(calls, [3, 2])
  assert.deepEqual(result.map((item) => item.globalSeq), [1, 2, 3])
})
