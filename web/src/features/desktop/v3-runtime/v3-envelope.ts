import type {
  ChatMessageRecord,
} from '../chat/types/chat'
import type {
  DesktopDaemonSnapshot,
  DesktopStateStatus,
} from '../state/desktop-state'
import type {
  DesktopRunIntentRecord,
} from '../types/realtime'

export type V3EnvelopeSourceKind =
  | 'persisted'
  | 'snapshot'
  | 'replay'
  | 'websocket'
  | 'optimistic'
  | 'http'
  | 'runtime'

export type V3EnvelopeTransport =
  | 'indexeddb'
  | 'http'
  | 'browser-websocket'
  | 'memory'

export interface V3EnvelopeSource {
  kind: V3EnvelopeSourceKind
  transport?: V3EnvelopeTransport
  name?: string
  requestId?: string
  subscriptionId?: string
}

export interface V3EnvelopeCursor {
  endpointCursor?: string
  stream?: string
  rev?: number
  prevRev?: number
  globalSeq?: number
  sourceSeq?: number
  highWatermarkSeq?: number
  tsUnixMs?: number
}

export type V3EnvelopeDomain =
  | 'session'
  | 'message'
  | 'assistant'
  | 'reasoning'
  | 'tool'
  | 'plan'
  | 'permission'
  | 'readiness'
  | 'usage'
  | 'run'
  | 'workspace'
  | 'notification'
  | 'preference'
  | 'agent-model-policy'
  | 'connection'
  | 'unknown'

export interface V3EnvelopeMeta {
  id: string
  source: V3EnvelopeSource
  receivedAt: number
  sessionId?: string
  entityId?: string
  eventType?: string
  domain: V3EnvelopeDomain
  cursor: V3EnvelopeCursor
}

export interface V3CanonicalEvent {
  type: string
  payload?: unknown
  stream?: string
  entityId?: string
  rev?: number
  prevRev?: number
  globalSeq?: number
  sourceSeq?: number
  tsUnixMs?: number
}

interface V3EnvelopeBase {
  meta: V3EnvelopeMeta
}

export interface V3SnapshotEnvelope extends V3EnvelopeBase {
  kind: 'snapshot'
  mode: 'replace' | 'merge'
  snapshot: DesktopDaemonSnapshot
}

export interface V3PersistedRestoreEnvelope extends V3EnvelopeBase {
  kind: 'persisted.restore'
  mode: 'replace' | 'merge'
  snapshot: DesktopDaemonSnapshot
  cursorsByScope?: Record<string, V3EnvelopeCursor>
}

export interface V3EventEnvelope extends V3EnvelopeBase {
  kind: 'event'
  event: V3CanonicalEvent
}

export interface V3OptimisticSendEnvelope extends V3EnvelopeBase {
  kind: 'optimistic.send'
  sessionId: string
  clientMessageId: string
  message: ChatMessageRecord
  runIntent?: DesktopRunIntentRecord | null
}

export interface V3ConnectionStatusEnvelope extends V3EnvelopeBase {
  kind: 'connection.status'
  status: DesktopStateStatus
  error?: string | null
}

export interface V3ConnectionStaleEnvelope extends V3EnvelopeBase {
  kind: 'connection.stale'
  reason: string
}

export type V3ControlKind =
  | 'keepalive'
  | 'replay.started'
  | 'replay.complete'
  | 'projection.high_watermark'
  | 'cursor.error'
  | 'auth.denied'
  | 'slow_consumer.reconnect_required'

export interface V3ControlEnvelope extends V3EnvelopeBase {
  kind: 'control'
  control: V3ControlKind
  reason?: string
  error?: string
}

export type V3Envelope =
  | V3SnapshotEnvelope
  | V3PersistedRestoreEnvelope
  | V3EventEnvelope
  | V3OptimisticSendEnvelope
  | V3ConnectionStatusEnvelope
  | V3ConnectionStaleEnvelope
  | V3ControlEnvelope

export interface V3EnvelopeOptions {
  id?: string
  source?: Partial<V3EnvelopeSource>
  receivedAt?: number
  sessionId?: string | null
  entityId?: string | null
  endpointCursor?: string | null
  stream?: string | null
  highWatermarkSeq?: number | null
}

export interface V3DurableEventEnvelopeOptions extends V3EnvelopeOptions {
  sourceKind?: Extract<V3EnvelopeSourceKind, 'replay' | 'websocket' | 'http' | 'runtime'>
}

export interface V3OptimisticSendInput extends V3EnvelopeOptions {
  sessionId?: string | null
  clientMessageId?: string | null
  message: ChatMessageRecord
  runIntent?: DesktopRunIntentRecord | null
}

export type V3EnvelopeValidation =
  | { ok: true }
  | { ok: false; reason: string }

interface DurableEventWire {
  type?: string
  kind?: string
  event_type?: string
  payload?: unknown
  protocol?: string
  protocol_version?: number
  session_id?: string
  event?: {
    id?: string
    session_id?: string
    event_type?: string
    payload?: unknown
    seq?: number
    ts_unix_ms?: number
  }
  stream?: string
  entity_id?: string
  endpoint_cursor?: string
  subscription_id?: string
  rev?: number
  prevRev?: number
  global_seq?: number
  source_seq?: number
  ts_unix_ms?: number
  high_watermark_seq?: number
  projection_high_watermark_seq?: number
  error?: string
  reason?: string
  error_code?: string
}

export function createV3SnapshotEnvelope(snapshot: DesktopDaemonSnapshot, options: V3EnvelopeOptions & { mode?: 'replace' | 'merge' } = {}): V3SnapshotEnvelope {
  const mode = options.mode ?? 'replace'
  const cursor = envelopeCursor({ rev: snapshot.rev, highWatermarkSeq: options.highWatermarkSeq })
  return {
    kind: 'snapshot',
    mode,
    snapshot,
    meta: createMeta({
      id: options.id ?? `snapshot:${mode}:rev:${String(snapshot.rev)}`,
      source: createSource('snapshot', { transport: 'http', ...(options.source ?? {}) }),
      receivedAt: options.receivedAt,
      domain: 'session',
      cursor,
      sessionId: normalizeOptionalString(options.sessionId),
      entityId: normalizeOptionalString(options.entityId),
    }),
  }
}

export function createV3PersistedRestoreEnvelope(snapshot: DesktopDaemonSnapshot, options: V3EnvelopeOptions & { mode?: 'replace' | 'merge'; cursorsByScope?: Record<string, V3EnvelopeCursor> } = {}): V3PersistedRestoreEnvelope {
  const mode = options.mode ?? 'replace'
  const cursor = envelopeCursor({ rev: snapshot.rev, highWatermarkSeq: options.highWatermarkSeq })
  return {
    kind: 'persisted.restore',
    mode,
    snapshot,
    cursorsByScope: options.cursorsByScope,
    meta: createMeta({
      id: options.id ?? `persisted.restore:${mode}:rev:${String(snapshot.rev)}`,
      source: createSource('persisted', { transport: 'indexeddb', ...(options.source ?? {}) }),
      receivedAt: options.receivedAt,
      domain: 'session',
      cursor,
      sessionId: normalizeOptionalString(options.sessionId),
      entityId: normalizeOptionalString(options.entityId),
    }),
  }
}

export function createV3EventEnvelope(event: V3CanonicalEvent, options: V3EnvelopeOptions = {}): V3EventEnvelope {
  const eventType = event.type.trim()
  const payload = asRecord(event.payload)
  const stream = normalizeOptionalString(event.stream ?? options.stream)
  const entityId = normalizeOptionalString(options.entityId) || normalizeOptionalString(event.entityId)
  const sessionId = normalizeOptionalString(options.sessionId)
    || sessionIdFromEventPayload(eventType, payload, entityId, stream)
  const cursor = envelopeCursor({
    endpointCursor: options.endpointCursor,
    stream,
    rev: event.rev,
    prevRev: event.prevRev,
    globalSeq: event.globalSeq,
    sourceSeq: event.sourceSeq,
    tsUnixMs: event.tsUnixMs,
    highWatermarkSeq: options.highWatermarkSeq,
  })
  const source = createSource('runtime', options.source)
  return {
    kind: 'event',
    event: {
      ...event,
      type: eventType,
      stream,
      entityId: entityId || undefined,
    },
    meta: createMeta({
      id: options.id ?? eventIdentity({ eventType, payload, sessionId, entityId, cursor, source }),
      source,
      receivedAt: options.receivedAt,
      sessionId,
      entityId,
      eventType,
      domain: classifyV3EventType(eventType),
      cursor,
    }),
  }
}

export function normalizeV3DurableEventEnvelope(raw: unknown, options: V3DurableEventEnvelopeOptions = {}): V3EventEnvelope {
  const record = asRecord(raw)
  const wire = record as DurableEventWire | null
  const nestedEvent = asRecord(wire?.event)
  const eventType = stringValue(wire?.event_type)
    || stringValue(nestedEvent?.event_type)
    || stringValue(wire?.type)
  const globalSeq = positiveInteger(wire?.global_seq) ?? positiveInteger(nestedEvent?.seq)
  const sourceSeq = positiveInteger(wire?.source_seq) ?? positiveInteger(nestedEvent?.seq)
  const tsUnixMs = nonNegativeInteger(wire?.ts_unix_ms) ?? nonNegativeInteger(nestedEvent?.ts_unix_ms)
  const stream = normalizeOptionalString(wire?.stream ?? options.stream)
  const entityId = normalizeOptionalString(options.entityId) || stringValue(wire?.entity_id)
  const payload = normalizeEventPayload(eventType, wire, nestedEvent, { globalSeq, sourceSeq, tsUnixMs })
  const source = createSource(options.sourceKind ?? 'runtime', options.source)
  return createV3EventEnvelope({
    type: eventType,
    payload,
    stream,
    entityId,
    rev: positiveInteger(wire?.rev),
    prevRev: nonNegativeInteger(wire?.prevRev),
    globalSeq,
    sourceSeq,
    tsUnixMs,
  }, {
    ...options,
    id: options.id || stringValue(nestedEvent?.id) || undefined,
    source,
    sessionId: normalizeOptionalString(options.sessionId),
    endpointCursor: normalizeOptionalString(wire?.endpoint_cursor ?? options.endpointCursor),
    highWatermarkSeq: options.highWatermarkSeq
      ?? positiveInteger(wire?.high_watermark_seq)
      ?? positiveInteger(wire?.projection_high_watermark_seq),
  })
}

export function normalizeV3RealtimeFrame(raw: unknown, options: V3EnvelopeOptions = {}): V3Envelope {
  const frame = parseObjectFrame(raw)
  const protocol = stringValue(frame.protocol)
  if (protocol && protocol !== 'v3.realtime') {
    throw new Error(`Unsupported V3 realtime protocol: ${protocol}`)
  }
  const kind = stringValue(frame.kind ?? frame.type)
  if (kind === 'event') {
    return normalizeV3DurableEventEnvelope(frame, {
      ...options,
      sourceKind: 'websocket',
      source: createSource('websocket', {
        transport: 'browser-websocket',
        subscriptionId: stringValue(frame.subscription_id),
        ...(options.source ?? {}),
      }),
      endpointCursor: normalizeOptionalString(frame.endpoint_cursor ?? options.endpointCursor),
      highWatermarkSeq: options.highWatermarkSeq
        ?? positiveInteger(frame.high_watermark_seq)
        ?? positiveInteger(frame.projection_high_watermark_seq),
    })
  }
  return createV3ControlEnvelope(kind as V3ControlKind, {
    ...options,
    source: createSource('websocket', {
      transport: 'browser-websocket',
      subscriptionId: stringValue(frame.subscription_id),
      ...(options.source ?? {}),
    }),
    sessionId: normalizeOptionalString(frame.session_id ?? frame.event?.session_id ?? options.sessionId),
    endpointCursor: normalizeOptionalString(frame.endpoint_cursor ?? options.endpointCursor),
    highWatermarkSeq: options.highWatermarkSeq
      ?? positiveInteger(frame.high_watermark_seq)
      ?? positiveInteger(frame.projection_high_watermark_seq),
  }, {
    reason: stringValue(frame.reason) || stringValue(frame.error_code),
    error: stringValue(frame.error),
  })
}

export function createV3OptimisticSendEnvelope(input: V3OptimisticSendInput): V3OptimisticSendEnvelope {
  const sessionId = normalizeOptionalString(input.sessionId) || input.message.sessionId.trim()
  const clientMessageId = normalizeOptionalString(input.clientMessageId) || input.message.id.trim()
  const source = createSource('optimistic', { transport: 'memory', ...(input.source ?? {}) })
  const cursor = envelopeCursor({})
  return {
    kind: 'optimistic.send',
    sessionId,
    clientMessageId,
    message: {
      ...input.message,
      sessionId,
    },
    runIntent: input.runIntent ?? null,
    meta: createMeta({
      id: input.id ?? `optimistic.send:${sessionId}:${clientMessageId}`,
      source,
      receivedAt: input.receivedAt,
      sessionId,
      domain: 'message',
      cursor,
    }),
  }
}

export function createV3ConnectionStatusEnvelope(status: DesktopStateStatus, options: V3EnvelopeOptions & { error?: string | null } = {}): V3ConnectionStatusEnvelope {
  const source = createSource('runtime', options.source)
  return {
    kind: 'connection.status',
    status,
    error: options.error ?? null,
    meta: createMeta({
      id: options.id ?? `connection.status:${status}:${options.receivedAt ?? ''}`,
      source,
      receivedAt: options.receivedAt,
      sessionId: normalizeOptionalString(options.sessionId),
      domain: 'connection',
      cursor: envelopeCursor({ endpointCursor: options.endpointCursor, highWatermarkSeq: options.highWatermarkSeq }),
    }),
  }
}

export function createV3ConnectionStaleEnvelope(reason: string, options: V3EnvelopeOptions = {}): V3ConnectionStaleEnvelope {
  const source = createSource('runtime', options.source)
  const normalizedReason = reason.trim()
  return {
    kind: 'connection.stale',
    reason: normalizedReason,
    meta: createMeta({
      id: options.id ?? `connection.stale:${normalizedReason || 'unknown'}:${options.receivedAt ?? ''}`,
      source,
      receivedAt: options.receivedAt,
      sessionId: normalizeOptionalString(options.sessionId),
      domain: 'connection',
      cursor: envelopeCursor({ endpointCursor: options.endpointCursor, highWatermarkSeq: options.highWatermarkSeq }),
    }),
  }
}

export function createV3ControlEnvelope(control: V3ControlKind, options: V3EnvelopeOptions = {}, details: { reason?: string; error?: string } = {}): V3ControlEnvelope {
  const source = createSource('runtime', options.source)
  return {
    kind: 'control',
    control,
    reason: details.reason,
    error: details.error,
    meta: createMeta({
      id: options.id ?? `control:${control}:${normalizeOptionalString(options.sessionId) || 'global'}:${options.endpointCursor ?? ''}`,
      source,
      receivedAt: options.receivedAt,
      sessionId: normalizeOptionalString(options.sessionId),
      domain: 'connection',
      cursor: envelopeCursor({ endpointCursor: options.endpointCursor, highWatermarkSeq: options.highWatermarkSeq }),
    }),
  }
}

export function validateV3Envelope(envelope: V3Envelope): V3EnvelopeValidation {
  if (!envelope.meta.id.trim()) {
    return { ok: false, reason: 'V3 envelope missing identity' }
  }
  if (!envelope.meta.source.kind) {
    return { ok: false, reason: 'V3 envelope missing source kind' }
  }
  if (!Number.isFinite(envelope.meta.receivedAt) || envelope.meta.receivedAt < 0) {
    return { ok: false, reason: 'V3 envelope missing valid receivedAt' }
  }
  switch (envelope.kind) {
    case 'snapshot':
    case 'persisted.restore':
      return validSnapshotRevision(envelope.snapshot.rev)
    case 'event':
      return validEventEnvelope(envelope)
    case 'optimistic.send':
      if (!envelope.sessionId.trim()) {
        return { ok: false, reason: 'optimistic send missing session id' }
      }
      if (!envelope.clientMessageId.trim() || !envelope.message.id.trim()) {
        return { ok: false, reason: 'optimistic send missing message identity' }
      }
      return { ok: true }
    case 'connection.status':
      if (!envelope.status) {
        return { ok: false, reason: 'connection status envelope missing status' }
      }
      return { ok: true }
    case 'connection.stale':
      if (!envelope.reason.trim()) {
        return { ok: false, reason: 'connection stale envelope missing reason' }
      }
      return { ok: true }
    case 'control':
      if (!isV3ControlKind(envelope.control)) {
        return { ok: false, reason: `unsupported V3 control envelope: ${envelope.control}` }
      }
      return { ok: true }
    default:
      return { ok: false, reason: 'unsupported V3 envelope kind' }
  }
}

export function classifyV3EventType(eventType: string): V3EnvelopeDomain {
  const normalized = eventType.trim()
  if (!normalized) {
    return 'unknown'
  }
  if (normalized === 'session.message.appended' || normalized.startsWith('desktop/message')) {
    return 'message'
  }
  if (normalized.startsWith('session.assistant')) {
    return 'assistant'
  }
  if (normalized.startsWith('session.reasoning')) {
    return 'reasoning'
  }
  if (normalized.startsWith('session.tool')) {
    return 'tool'
  }
  if (normalized.startsWith('session.run') || normalized === 'session.lifecycle.updated' || normalized === 'session.run_intent.recorded' || normalized.startsWith('desktop/run-intent')) {
    return 'run'
  }
  if (normalized.startsWith('permission.') || normalized.startsWith('desktop/permission')) {
    return 'permission'
  }
  if (normalized.startsWith('plan.') || normalized.startsWith('session.plan') || normalized.startsWith('desktop/plan')) {
    return 'plan'
  }
  if (normalized.startsWith('desktop/route-readiness')) {
    return 'readiness'
  }
  if (normalized === 'run.usage.updated' || normalized.startsWith('desktop/usage')) {
    return 'usage'
  }
  if (normalized.startsWith('desktop/workspace')) {
    return 'workspace'
  }
  if (normalized.startsWith('desktop/notification')) {
    return 'notification'
  }
  if (normalized.startsWith('desktop/preference')) {
    return 'preference'
  }
  if (normalized.startsWith('desktop/agent-model-policy')) {
    return 'agent-model-policy'
  }
  if (normalized.startsWith('desktop/session') || normalized.startsWith('session.')) {
    return 'session'
  }
  return 'unknown'
}

function createMeta(input: {
  id: string
  source: V3EnvelopeSource
  receivedAt?: number
  sessionId?: string
  entityId?: string
  eventType?: string
  domain: V3EnvelopeDomain
  cursor: V3EnvelopeCursor
}): V3EnvelopeMeta {
  return {
    id: input.id.trim(),
    source: input.source,
    receivedAt: normalizeReceivedAt(input.receivedAt),
    sessionId: input.sessionId || undefined,
    entityId: input.entityId || undefined,
    eventType: input.eventType || undefined,
    domain: input.domain,
    cursor: input.cursor,
  }
}

function createSource(kind: V3EnvelopeSourceKind, source: Partial<V3EnvelopeSource> | undefined): V3EnvelopeSource {
  return {
    kind: source?.kind ?? kind,
    transport: source?.transport,
    name: source?.name,
    requestId: source?.requestId,
    subscriptionId: source?.subscriptionId,
  }
}

function envelopeCursor(input: {
  endpointCursor?: string | null
  stream?: string | null
  rev?: number | null
  prevRev?: number | null
  globalSeq?: number | null
  sourceSeq?: number | null
  highWatermarkSeq?: number | null
  tsUnixMs?: number | null
}): V3EnvelopeCursor {
  const cursor: V3EnvelopeCursor = {}
  const endpointCursor = normalizeOptionalString(input.endpointCursor)
  const stream = normalizeOptionalString(input.stream)
  const rev = positiveInteger(input.rev)
  const prevRev = nonNegativeInteger(input.prevRev)
  const globalSeq = positiveInteger(input.globalSeq)
  const sourceSeq = positiveInteger(input.sourceSeq)
  const highWatermarkSeq = nonNegativeInteger(input.highWatermarkSeq)
  const tsUnixMs = nonNegativeInteger(input.tsUnixMs)
  if (endpointCursor) cursor.endpointCursor = endpointCursor
  if (stream) cursor.stream = stream
  if (rev !== undefined) cursor.rev = rev
  if (prevRev !== undefined) cursor.prevRev = prevRev
  if (globalSeq !== undefined) cursor.globalSeq = globalSeq
  if (sourceSeq !== undefined) cursor.sourceSeq = sourceSeq
  if (highWatermarkSeq !== undefined) cursor.highWatermarkSeq = highWatermarkSeq
  if (tsUnixMs !== undefined) cursor.tsUnixMs = tsUnixMs
  return cursor
}

function eventIdentity(input: {
  eventType: string
  payload: Record<string, unknown> | null
  sessionId: string
  entityId: string
  cursor: V3EnvelopeCursor
  source: V3EnvelopeSource
}): string {
  const payload = input.payload
  const stableEntity = input.entityId
    || input.sessionId
    || payloadString(payload, 'id')
    || payloadString(payload, 'session_id')
    || payloadString(payloadRecord(payload, 'message'), 'id')
    || payloadString(payloadRecord(payload, 'permission'), 'id')
    || 'global'
  const sequence = input.cursor.globalSeq !== undefined
    ? `global:${input.cursor.globalSeq}`
    : input.cursor.sourceSeq !== undefined
      ? `source:${input.cursor.sourceSeq}`
      : input.cursor.rev !== undefined
        ? `rev:${input.cursor.rev}`
        : input.cursor.tsUnixMs !== undefined
          ? `ts:${input.cursor.tsUnixMs}`
          : `source:${input.source.kind}`
  return `${input.source.kind}:${input.eventType || 'unknown'}:${stableEntity}:${sequence}`
}

function normalizeEventPayload(eventType: string, wire: DurableEventWire | null, nestedEvent: Record<string, unknown> | null, sequence: { globalSeq?: number; sourceSeq?: number; tsUnixMs?: number }): unknown {
  const payload = asRecord(wire?.payload) ?? asRecord(nestedEvent?.payload) ?? asRecord(wire) ?? {}
  return {
    ...payload,
    event_type: payloadString(payload, 'event_type') || eventType,
    global_seq: payload.global_seq ?? sequence.globalSeq,
    source_seq: payload.source_seq ?? sequence.sourceSeq,
    ts_unix_ms: payload.ts_unix_ms ?? sequence.tsUnixMs,
  }
}

function validSnapshotRevision(rev: unknown): V3EnvelopeValidation {
  return typeof rev === 'number' && Number.isFinite(rev) && rev >= 0
    ? { ok: true }
    : { ok: false, reason: 'snapshot envelope missing valid rev' }
}

function validEventEnvelope(envelope: V3EventEnvelope): V3EnvelopeValidation {
  if (!envelope.event.type.trim()) {
    return { ok: false, reason: 'event envelope missing event type' }
  }
  if (envelope.event.rev !== undefined && positiveInteger(envelope.event.rev) === undefined) {
    return { ok: false, reason: 'event envelope has invalid rev' }
  }
  if (envelope.event.prevRev !== undefined && nonNegativeInteger(envelope.event.prevRev) === undefined) {
    return { ok: false, reason: 'event envelope has invalid prevRev' }
  }
  return { ok: true }
}

function isV3ControlKind(control: string): control is V3ControlKind {
  return control === 'keepalive'
    || control === 'replay.started'
    || control === 'replay.complete'
    || control === 'projection.high_watermark'
    || control === 'cursor.error'
    || control === 'auth.denied'
    || control === 'slow_consumer.reconnect_required'
}

function parseObjectFrame(raw: unknown): DurableEventWire {
  if (typeof raw === 'string') {
    const parsed = JSON.parse(raw) as unknown
    if (!isRecord(parsed)) {
      throw new Error('V3 realtime frame is not an object')
    }
    return parsed as DurableEventWire
  }
  if (!isRecord(raw)) {
    throw new Error('V3 realtime frame is not an object')
  }
  return raw as DurableEventWire
}

function sessionIdFromEventPayload(eventType: string, payload: Record<string, unknown> | null, entityId: string, stream: string): string {
  if (eventType.startsWith('session.') || eventType.startsWith('permission.')) {
    const streamSessionId = sessionIdFromStream(stream)
    if (entityId) return entityId
    if (streamSessionId) return streamSessionId
  }
  return payloadString(payload, 'session_id')
    || payloadString(payload, 'sessionId')
    || (eventType.startsWith('session.') ? payloadString(payload, 'id') : '')
    || payloadString(payloadRecord(payload, 'session'), 'id')
    || payloadString(payloadRecord(payload, 'message'), 'session_id')
    || payloadString(payloadRecord(payload, 'message'), 'sessionId')
    || payloadString(payloadRecord(payload, 'permission'), 'session_id')
    || payloadString(payloadRecord(payload, 'permission'), 'sessionId')
}

function sessionIdFromStream(stream: string): string {
  if (stream.startsWith('session:')) {
    return stream.slice('session:'.length).trim()
  }
  if (stream.startsWith('v3/session:')) {
    return stream.slice('v3/session:'.length).trim()
  }
  return ''
}

function normalizeReceivedAt(receivedAt: number | undefined): number {
  return typeof receivedAt === 'number' && Number.isFinite(receivedAt) && receivedAt >= 0
    ? Math.floor(receivedAt)
    : Date.now()
}

function normalizeOptionalString(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function payloadString(record: Record<string, unknown> | null, key: string): string {
  return stringValue(record?.[key])
}

function payloadRecord(record: Record<string, unknown> | null, key: string): Record<string, unknown> | null {
  return asRecord(record?.[key])
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return isRecord(value) ? value : null
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function positiveInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value > 0 ? Math.floor(value) : undefined
}

function nonNegativeInteger(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0 ? Math.floor(value) : undefined
}
