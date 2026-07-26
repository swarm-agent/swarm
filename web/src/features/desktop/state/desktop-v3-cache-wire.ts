import type {
  CacheEvent,
  DesktopV3CacheAction,
  RealtimeMessage,
  SessionCreateMutationResponse,
  SessionEventPayload,
  SessionMessageMutationResponse,
  SessionMutationErrorResponse,
  MessageMutationConflictResponse,
  SessionsReconnectResponse,
  SyncSnapshotResponse,
  SyncStreamEvent,
  SyncStreamResponse,
  V3RealtimeOutboxRecord,
  V3SessionEvent,
} from './desktop-v3-cache-types'

const SUPPORTED_REALTIME_KINDS = new Set([
  'hello',
  'event',
  'replay.started',
  'replay.complete',
  'cursor.error',
  'keepalive',
  'endpoint.watermark',
  'projection.high_watermark',
  'workset.session.discovered',
  'workset.session.updated',
  'workset.session.removed',
  'auth.denied',
  'slow_consumer.reconnect_required',
  'live.patch',
  'notification.resource.updated',
  'task.lifecycle.updated',
])

export function assertDesktopV3RealtimeFrame(frame: RealtimeMessage): void {
  if (frame.protocol !== 'v3.realtime' || frame.protocol_version !== 1) {
    throw new Error('protocol invalid: realtime frame must use v3.realtime version 1')
  }
  const kind = stringValue(frame.kind)
  if (!SUPPORTED_REALTIME_KINDS.has(kind)) {
    throw new Error(`protocol invalid: unsupported realtime frame kind ${kind || '<empty>'}`)
  }
  const type = stringValue(frame.type)
  if (type && type !== kind) {
    throw new Error(`protocol invalid: realtime kind/type mismatch ${kind}/${type}`)
  }
  if (kind === 'notification.resource.updated' || kind === 'task.lifecycle.updated') return

  const event = frame.event
  const payload = event ? eventPayloadRecord(event) : undefined
  const identities = [
    ['frame.session_id', frame.session_id],
    ['event.session_id', event?.session_id],
    ['projection.session_id', frame.projection?.session_id],
    ['session.id', frame.session?.id],
    ['current_run_state.session_id', frame.current_run_state?.session_id],
    ['payload.session.id', recordValue(payload?.session)?.id],
    ['payload.message.session_id', recordValue(payload?.message)?.session_id],
    ['payload.run_intent.session_id', recordValue(payload?.run_intent)?.session_id],
    ['payload.permission.session_id', recordValue(payload?.permission)?.session_id],
    ['payload.permission_summary.session_id', recordValue(payload?.permission_summary)?.session_id],
  ] as const
  assertConsistentSessionIdentities(`realtime ${kind}`, identities)
  if (frame.event_type && event?.event_type && frame.event_type !== event.event_type) {
    throw new Error('protocol invalid: realtime frame/event event_type mismatch')
  }
}

export function assertDesktopV3SnapshotIdentities(snapshot: SyncSnapshotResponse | SessionsReconnectResponse): void {
  assertIdentityMap(snapshot.sessions_by_id, 'sessions_by_id', (value) => value.id)
  assertIdentityMap(snapshot.projections_by_session, 'projections_by_session', (value) => value.session_id)
  assertIdentityListMap(snapshot.messages_by_session, 'messages_by_session', (value) => value.session_id)
  assertIdentityListMap(snapshot.events_by_session, 'events_by_session', (value) => value.session_id)
  assertIdentityListMap(snapshot.run_intents_by_session, 'run_intents_by_session', (value) => value.session_id)
  assertIdentityMap(snapshot.current_run_state_by_session, 'current_run_state_by_session', (value) => value.session_id)
  assertIdentityMap(snapshot.permission_summaries_by_session, 'permission_summaries_by_session', (value) => stringValue(value.session_id ?? value.sessionId), true)
  if ('tombstones_by_session' in snapshot) {
    assertIdentityMap(snapshot.tombstones_by_session, 'tombstones_by_session', (value) => value.session_id)
  }

  if ('current_run_intent_by_session' in snapshot) {
    assertIdentityMap(snapshot.current_run_intent_by_session, 'current_run_intent_by_session', (value) => value.session_id)
  }
}

function assertIdentityMap<T>(
  values: Record<string, T> | undefined,
  label: string,
  identity: (value: T) => unknown,
  allowMissing = false,
): void {
  for (const [key, value] of Object.entries(values ?? {})) {
    const nested = stringValue(identity(value))
    if ((!nested && !allowMissing) || (nested && nested !== key)) {
      throw new Error(`protocol invalid: ${label} key ${key} conflicts with nested identity ${nested || '<empty>'}`)
    }
  }
}

function assertIdentityListMap<T>(
  values: Record<string, T[]> | undefined,
  label: string,
  identity: (value: T) => unknown,
): void {
  for (const [key, items] of Object.entries(values ?? {})) {
    for (const item of items ?? []) {
      const nested = stringValue(identity(item))
      if (!nested || nested !== key) {
        throw new Error(`protocol invalid: ${label} key ${key} conflicts with nested identity ${nested || '<empty>'}`)
      }
    }
  }
}

function assertConsistentSessionIdentities(label: string, identities: ReadonlyArray<readonly [string, unknown]>): void {
  let expected = ''
  let expectedLabel = ''
  for (const [identityLabel, value] of identities) {
    const identity = stringValue(value)
    if (!identity) continue
    if (!expected) {
      expected = identity
      expectedLabel = identityLabel
      continue
    }
    if (identity !== expected) {
      throw new Error(`protocol invalid: ${label} ${identityLabel} ${identity} conflicts with ${expectedLabel} ${expected}`)
    }
  }
}

function eventPayloadRecord(event: V3SessionEvent): Record<string, unknown> | undefined {
  if (!event.payload) return undefined
  if (typeof event.payload === 'string') {
    const parsed = JSON.parse(event.payload) as unknown
    return recordValue(parsed)
  }
  return recordValue(event.payload)
}

function recordValue(value: unknown): Record<string, unknown> | undefined {
  return value !== null && typeof value === 'object' && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined
}

function stringValue(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

export function bootstrapResponseToAction(raw: SyncSnapshotResponse): DesktopV3CacheAction {
  assertDesktopV3SnapshotIdentities(raw)
  return {
    type: 'snapshot.apply',
    source: 'bootstrap',
    scopeId: raw.scope_id,
    snapshot: raw,
  }
}

export function selectSession(sessionId?: string): DesktopV3CacheAction {
  return {
    type: 'session.select',
    sessionId: sessionId?.trim() || undefined,
  }
}

export function hydrateResponseToAction(
  raw: SyncSnapshotResponse,
  requestedSessionIds: string[],
): DesktopV3CacheAction {
  assertDesktopV3SnapshotIdentities(raw)
  return {
    type: 'hydrate.apply',
    source: 'hydrate',
    scopeId: raw.scope_id,
    requestedSessionIds,
    snapshot: raw,
  }
}

export function syncStreamResponseToAction(
  raw: SyncStreamResponse,
  scopeId: string,
): DesktopV3CacheAction {
  for (const event of raw.events) assertSyncStreamEventIdentities(event)
  return {
    type: 'syncStream.applyBatch',
    scopeId,
    endpointCursor: raw.endpoint_cursor,
    events: raw.events.map(normalizeSyncStreamEvent),
    hasMore: raw.has_more,
    replayInstructions: raw.replay_instructions,
  }
}

export function reconnectResponseToActions(raw: SessionsReconnectResponse): DesktopV3CacheAction[] {
  assertDesktopV3SnapshotIdentities(raw)
  const actions: DesktopV3CacheAction[] = [
    {
      type: 'reconnect.applySnapshot',
      snapshot: raw,
    },
  ]

  if (raw.realtime?.resume) {
    actions.push({
      type: 'realtime.storeResume',
      streamPath: raw.realtime.stream_path,
      resume: raw.realtime.resume,
    })
  }

  return actions
}

export function realtimeFrameToActions(frame: RealtimeMessage): DesktopV3CacheAction[] {
  assertDesktopV3RealtimeFrame(frame)
  switch (frame.kind) {
    case 'event':
      return [{ type: 'realtime.applyEvent', event: normalizeRealtimeEventFrame(frame), endpointCursor: frame.endpoint_cursor }]

    case 'notification.resource.updated':
      return [{ type: 'realtime.applyNotificationResource', frame }]

    case 'task.lifecycle.updated':
      return [{ type: 'realtime.applyAITaskResource', frame }]

    case 'workset.session.discovered':
      return worksetFrameToActions(frame, { type: 'realtime.worksetSessionDiscovered', frame })

    case 'workset.session.updated':
      return worksetFrameToActions(frame, { type: 'realtime.worksetSessionUpdated', frame })

    case 'workset.session.removed':
      return worksetFrameToActions(frame, { type: 'realtime.worksetSessionRemoved', frame })

    case 'cursor.error':
      return [{ type: 'realtime.cursorError', frame }]

    case 'hello':
    case 'keepalive':
    case 'endpoint.watermark':
    case 'projection.high_watermark':
    case 'replay.started':
    case 'replay.complete':
    case 'auth.denied':
    case 'slow_consumer.reconnect_required':
      return [{ type: 'realtime.control', frame }]

    default:
      return [{ type: 'realtime.unknownFrame', frame }]
  }
}

function worksetFrameToActions(frame: RealtimeMessage, membershipAction: DesktopV3CacheAction): DesktopV3CacheAction[] {
  const actions: DesktopV3CacheAction[] = []
  if (frame.event) {
    actions.push({
      type: 'realtime.applyEvent',
      event: normalizeRealtimeEventFrame(frame),
      endpointCursor: frame.endpoint_cursor,
    })
  }
  actions.push(membershipAction)
  return actions
}

export function sessionCreateResponseToAction(
  raw: SessionCreateMutationResponse | SessionMutationErrorResponse,
  sidebarScopeId: string,
): DesktopV3CacheAction {
  return {
    type: 'mutation.sessionCreateResult',
    raw,
    sidebarScopeId,
  }
}

export function messageMutationResponseToAction(
  raw: SessionMessageMutationResponse | MessageMutationConflictResponse,
  clientRequestId: string,
  messageId: string,
): DesktopV3CacheAction {
  return {
    type: 'mutation.messageResult',
    raw,
    clientRequestId,
    messageId,
  }
}

export function normalizeSyncStreamEvent(raw: SyncStreamEvent): CacheEvent {
  assertSyncStreamEventIdentities(raw)
  return {
    source: 'sync-stream',
    sessionId: raw.session_id,
    eventType: raw.event_type,
    sessionEvent: raw.event,
    projection: raw.projection,
    payload: decodeSessionEventPayload(raw.event),
    notification: raw.notification,
    notificationSummary: raw.notification_summary,
    task: raw.task,
  }
}

export function normalizeRealtimeEventFrame(frame: RealtimeMessage): CacheEvent {
  assertDesktopV3RealtimeFrame(frame)
  const event = frame.event
  const sessionId = frame.session_id || event?.session_id || ''
  const eventType = frame.event_type || event?.event_type || ''
  return {
    source: 'realtime',
    sessionId,
    eventType,
    sessionEvent: event,
    projection: frame.projection,
    payload: event ? decodeSessionEventPayload(event) : {},
    notification: frame.notification,
    notificationSummary: frame.notification_summary,
    task: frame.task,
  }
}

export function outboxRecordToCacheEvent(raw: V3RealtimeOutboxRecord): CacheEvent {
  assertConsistentSessionIdentities('realtime outbox', [
    ['outbox.session_id', raw.session_id],
    ['event.session_id', raw.event?.session_id],
    ['projection.session_id', raw.projection?.session_id],
  ])
  return {
    source: 'outbox',
    sessionId: raw.session_id,
    eventType: raw.event.event_type,
    sessionEvent: raw.event,
    projection: raw.projection,
    payload: decodeSessionEventPayload(raw.event),
  }
}

function assertSyncStreamEventIdentities(raw: SyncStreamEvent): void {
  assertConsistentSessionIdentities('sync stream event', [
    ['envelope.session_id', raw.session_id],
    ['event.session_id', raw.event?.session_id],
    ['projection.session_id', raw.projection?.session_id],
  ])
  if (raw.event_type && raw.event?.event_type && raw.event_type !== raw.event.event_type) {
    throw new Error('protocol invalid: sync stream envelope/event event_type mismatch')
  }
  const payload = raw.event ? eventPayloadRecord(raw.event) : undefined
  assertConsistentSessionIdentities('sync stream event payload', [
    ['envelope.session_id', raw.session_id],
    ['payload.session.id', recordValue(payload?.session)?.id],
    ['payload.message.session_id', recordValue(payload?.message)?.session_id],
    ['payload.run_intent.session_id', recordValue(payload?.run_intent)?.session_id],
    ['payload.permission.session_id', recordValue(payload?.permission)?.session_id],
  ])
}

export function decodeSessionEventPayload(event: V3SessionEvent): SessionEventPayload {
  let payload: SessionEventPayload = {}
  if (event.payload) {
    payload = typeof event.payload === 'string'
      ? JSON.parse(event.payload) as SessionEventPayload
      : event.payload as SessionEventPayload
  }

  const payloadEpochId = typeof payload.epoch_id === 'string' ? payload.epoch_id.trim() : ''
  const eventEpochId = event.epoch_id?.trim() || ''
  const epochId = event.execution_epoch?.epoch_id?.trim() || payloadEpochId || eventEpochId
  const payloadOrdinal = typeof payload.ordinal === 'number' ? payload.ordinal : undefined
  const payloadParentEpochId = typeof payload.parent_epoch_id === 'string' ? payload.parent_epoch_id : undefined
  const inferredEpoch = epochId
    ? {
        ...event.execution_epoch,
        epoch_id: epochId,
        epoch_ordinal: event.execution_epoch?.epoch_ordinal ?? payloadOrdinal,
        session_id: event.session_id,
      }
    : undefined
  const inferredBoundary = event.event_type === 'execution_epoch.began' && epochId
    ? {
        epoch_id: epochId,
        epoch_ordinal: inferredEpoch?.epoch_ordinal,
        kind: 'started',
        parent_epoch_id: payloadParentEpochId,
      }
    : undefined

  return {
    ...payload,
    execution_epoch: payload.execution_epoch ?? inferredEpoch,
    execution_epoch_boundary: payload.execution_epoch_boundary ?? event.execution_epoch_boundary ?? inferredBoundary,
  }
}
