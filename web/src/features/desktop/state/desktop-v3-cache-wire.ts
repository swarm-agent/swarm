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

export function bootstrapResponseToAction(raw: SyncSnapshotResponse): DesktopV3CacheAction {
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
  switch (frame.kind) {
    case 'event':
      return [{ type: 'realtime.applyEvent', event: normalizeRealtimeEventFrame(frame), endpointCursor: frame.endpoint_cursor }]

    case 'workset.session.discovered':
      return [{ type: 'realtime.worksetSessionDiscovered', frame }]

    case 'workset.session.updated':
      return [{ type: 'realtime.worksetSessionUpdated', frame }]

    case 'workset.session.removed':
      return [{ type: 'realtime.worksetSessionRemoved', frame }]

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
  return {
    source: 'sync-stream',
    sessionId: raw.session_id,
    eventType: raw.event_type,
    sessionEvent: raw.event,
    projection: raw.projection,
    payload: decodeSessionEventPayload(raw.event),
  }
}

export function normalizeRealtimeEventFrame(frame: RealtimeMessage): CacheEvent {
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
  }
}

export function outboxRecordToCacheEvent(raw: V3RealtimeOutboxRecord): CacheEvent {
  return {
    source: 'outbox',
    sessionId: raw.session_id,
    eventType: raw.event.event_type,
    sessionEvent: raw.event,
    projection: raw.projection,
    payload: decodeSessionEventPayload(raw.event),
  }
}

export function decodeSessionEventPayload(event: V3SessionEvent): SessionEventPayload {
  if (!event.payload) return {}
  if (typeof event.payload === 'string') return JSON.parse(event.payload) as SessionEventPayload
  return event.payload as SessionEventPayload
}
