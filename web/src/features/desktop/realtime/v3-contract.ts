export const V3_REALTIME_STREAM_PATH = '/v3/realtime/stream' as const
export const V3_REALTIME_PROTOCOL = 'v3.realtime' as const
export const V3_REALTIME_PROTOCOL_VERSION = 1 as const

export const V3_REALTIME_KINDS = [
  'event',
  'replay.started',
  'replay.complete',
  'cursor.error',
  'keepalive',
  'projection.high_watermark',
  'subscribe.session',
  'unsubscribe.session',
  'resume',
  'auth.denied',
  'slow_consumer.reconnect_required',
] as const

export type V3RealtimeKind = (typeof V3_REALTIME_KINDS)[number]

export type V3RealtimeEvent = {
  id?: string
  session_id: string
  seq: number
  event_type: string
  payload: unknown
  ts_unix_ms?: number
  causation_id?: string
  correlation_id?: string
}

export type V3RealtimeProjection = {
  session_id?: string
  last_event_seq?: number
  projection_high_watermark_seq?: number
  updated_at?: number
}

export type V3RealtimeMessage = {
  protocol: typeof V3_REALTIME_PROTOCOL
  protocol_version: typeof V3_REALTIME_PROTOCOL_VERSION
  kind: V3RealtimeKind
  session_id?: string
  subscription_id?: string
  after_seq?: number
  last_seq?: number
  next_seq?: number
  high_watermark_seq?: number
  endpoint_cursor?: string
  event_type?: string
  event?: V3RealtimeEvent
  projection?: V3RealtimeProjection
  error_code?: string
  error?: string
  reason?: string
}

export function validateV3RealtimeMessage(message: unknown): asserts message is V3RealtimeMessage {
  if (!message || typeof message !== 'object') {
    throw new Error('v3 realtime message must be an object')
  }
  const record = message as Record<string, unknown>
  if (record.protocol !== V3_REALTIME_PROTOCOL) {
    throw new Error('v3 realtime message protocol mismatch')
  }
  if (record.protocol_version !== V3_REALTIME_PROTOCOL_VERSION) {
    throw new Error('v3 realtime message protocol_version mismatch')
  }
  if (!V3_REALTIME_KINDS.includes(record.kind as V3RealtimeKind)) {
    throw new Error(`unsupported v3 realtime kind ${String(record.kind)}`)
  }
  if (record.kind === 'event') {
    validateV3RealtimeEventMessage(record)
  }
}

function validateV3RealtimeEventMessage(record: Record<string, unknown>): void {
  const sessionId = typeof record.session_id === 'string' ? record.session_id.trim() : ''
  if (!sessionId) {
    throw new Error('v3 realtime event requires session_id')
  }
  if (typeof record.endpoint_cursor !== 'string' || record.endpoint_cursor.trim() === '') {
    throw new Error('v3 realtime event requires endpoint_cursor')
  }
  const event = record.event as Record<string, unknown> | undefined
  if (!event || typeof event !== 'object') {
    throw new Error('v3 realtime event requires event')
  }
  if (event.session_id !== sessionId) {
    throw new Error('v3 realtime event session_id conflict')
  }
  if (typeof event.seq !== 'number' || event.seq <= 0) {
    throw new Error('v3 realtime event requires event.seq')
  }
  if (record.last_seq !== undefined && record.last_seq !== event.seq) {
    throw new Error('v3 realtime event last_seq must equal event.seq')
  }
  if (typeof event.event_type !== 'string' || event.event_type.trim() === '') {
    throw new Error('v3 realtime event requires event_type')
  }
  if (record.event_type !== event.event_type) {
    throw new Error('v3 realtime event_type conflict')
  }
  if (event.event_type.startsWith('session.tool.')) {
    const payload = event.payload as Record<string, unknown> | undefined
    for (const key of ['run_id', 'step_id', 'call_id', 'tool_instance_id']) {
      if (!payload || typeof payload[key] !== 'string' || String(payload[key]).trim() === '') {
        throw new Error(`v3 realtime tool event requires ${key}`)
      }
    }
  }
}
