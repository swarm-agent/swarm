import assert from 'node:assert/strict'
import test from 'node:test'

import {
  V3_REALTIME_PROTOCOL,
  V3_REALTIME_PROTOCOL_VERSION,
  V3_REALTIME_STREAM_PATH,
  validateV3RealtimeMessage,
  type V3RealtimeMessage,
} from './v3-contract'

function validEventMessage(): V3RealtimeMessage {
  return {
    protocol: V3_REALTIME_PROTOCOL,
    protocol_version: V3_REALTIME_PROTOCOL_VERSION,
    kind: 'event',
    session_id: 'session-a',
    last_seq: 3,
    high_watermark_seq: 3,
    endpoint_cursor: 'cursor-3',
    event_type: 'session.message.appended',
    event: {
      id: 'event-3',
      session_id: 'session-a',
      seq: 3,
      event_type: 'session.message.appended',
      payload: { kind: 'message' },
      ts_unix_ms: 1234,
    },
  }
}

test('V3 realtime contract freezes the native route and protocol envelope', () => {
  assert.equal(V3_REALTIME_STREAM_PATH, '/v3/realtime/stream')
  const message = validEventMessage()
  const decoded = JSON.parse(JSON.stringify(message))
  validateV3RealtimeMessage(decoded)
  assert.equal(decoded.protocol, 'v3.realtime')
  assert.equal(decoded.protocol_version, 1)
})

test('V3 realtime TS contract accepts interleaved sessions by session_id and event.seq', () => {
  const frames = [
    validEventMessage(),
    {
      ...validEventMessage(),
      session_id: 'session-b',
      last_seq: 1,
      endpoint_cursor: 'cursor-4',
      event: { ...validEventMessage().event, session_id: 'session-b', seq: 1 },
    },
    {
      ...validEventMessage(),
      last_seq: 4,
      endpoint_cursor: 'cursor-5',
      event: { ...validEventMessage().event, seq: 4 },
    },
  ]
  const applied = new Map<string, number>()
  for (const frame of frames) {
    validateV3RealtimeMessage(frame)
    applied.set(frame.session_id ?? '', frame.event?.seq ?? 0)
  }
  assert.deepEqual([...applied.entries()], [
    ['session-a', 4],
    ['session-b', 1],
  ])
})

test('V3 realtime TS contract rejects old or ambiguous messages', () => {
  const cases: unknown[] = [
    { ...validEventMessage(), protocol: undefined },
    { ...validEventMessage(), protocol_version: 2 },
    { ...validEventMessage(), session_id: '' },
    { ...validEventMessage(), kind: 'sessionV3StreamFrame' },
    { ...validEventMessage(), event: { ...validEventMessage().event, seq: 0 } },
    { ...validEventMessage(), event: { ...validEventMessage().event, session_id: 'session-b' } },
    {
      ...validEventMessage(),
      event_type: 'session.tool.delta',
      event: {
        ...validEventMessage().event,
        event_type: 'session.tool.delta',
        payload: { run_id: 'run-1', call_id: 'call-1', delta: 'chunk' },
      },
    },
  ]
  for (const item of cases) {
    assert.throws(() => validateV3RealtimeMessage(item))
  }
})
