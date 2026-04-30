import assert from 'node:assert/strict'
import test from 'node:test'

import { appendLiveAssistantSegment } from './live-assistant-segments'

test('appends flushed assistant draft as a stable timeline segment', () => {
  const segments = appendLiveAssistantSegment([], ' First streamed answer.\n\nSecond sentence. ', 1234, 9)

  assert.equal(segments.length, 1)
  assert.equal(segments[0]?.id, 'live-assistant:1234:9:0')
  assert.equal(segments[0]?.content, 'First streamed answer.\n\nSecond sentence.')
  assert.equal(segments[0]?.createdAt, 1234)
  assert.equal(segments[0]?.seq, 9)
})

test('keeps retained assistant segment before later appended objects', () => {
  const segments = appendLiveAssistantSegment([], 'assistant preface', 10, 1)
  const timeline = [
    ...segments.map((segment) => ({ type: 'live-assistant', id: segment.id, content: segment.content })),
    { type: 'live-tool', id: 'tool:search' },
  ]

  assert.equal(timeline[0]?.type, 'live-assistant')
  assert.equal(timeline[0]?.content, 'assistant preface')
  assert.equal(timeline[1]?.type, 'live-tool')
})
